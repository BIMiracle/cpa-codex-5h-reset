// Package routing balances Codex credentials by weekly and five-hour quota while retaining
// a caller-scoped, model-scoped binding for the lifetime of each active session.
package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
)

type Request struct {
	Provider   string
	Providers  []string
	Model      string
	Options    Options
	Candidates []Candidate
}

type Options struct {
	Headers  map[string][]string
	Metadata map[string]any
}

type Candidate struct {
	ID         string
	Provider   string
	Priority   int
	Status     string
	Attributes map[string]string
}

type Response struct {
	Handled      bool
	AuthID       string `json:",omitempty"`
	Reject       bool   `json:",omitempty"`
	RejectCode   string `json:",omitempty"`
	RejectReason string `json:",omitempty"`
}

type Host interface {
	ListAuths() ([]keeper.Auth, error)
	FetchQuota(keeper.Auth, time.Time) (keeper.Quota, error)
	Log(string, string, map[string]any)
}

type sessionBinding struct {
	AuthID    string    `json:"auth_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

const (
	quotaBalanceTolerance   = 5.0
	weeklyRebindTolerance   = 10.0
	fiveHourRebindTolerance = 20.0
)

type diskState struct {
	Schema   int                       `json:"schema"`
	Bindings map[string]sessionBinding `json:"bindings"`
	Last     map[string]string         `json:"last,omitempty"`
}

type Plan struct {
	config Config
	loaded map[string]sessionBinding
	last   map[string]string
}

type Router struct {
	mu         sync.Mutex
	refreshMu  sync.Mutex
	host       Host
	now        func() time.Time
	config     Config
	bindings   map[string]sessionBinding
	quotas     map[string]keeper.Quota
	queried    map[string]time.Time
	last       map[string]string
	stateError string
	lastWrite  time.Time
	loadedFile string
	generation uint64
}

func New(h Host) *Router {
	return &Router{host: h, now: time.Now, bindings: map[string]sessionBinding{}, quotas: map[string]keeper.Quota{}, queried: map[string]time.Time{}, last: map[string]string{}}
}

// Prepare validates persisted input before the caller changes any live settings.
func (r *Router) Prepare(c Config) (Plan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := Plan{config: c}
	if !c.Enabled || r.loadedFile == c.StateFile {
		return p, nil
	}
	raw, err := os.ReadFile(c.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		p.loaded = map[string]sessionBinding{}
		return p, nil
	}
	if err != nil {
		return Plan{}, errors.New("routing_state_read_failed")
	}
	var state diskState
	if json.Unmarshal(raw, &state) != nil || state.Schema != 1 || len(state.Bindings) > 4096 || len(state.Last) > 4096 {
		return Plan{}, errors.New("invalid_routing_state")
	}
	for key, binding := range state.Bindings {
		decoded, err := hex.DecodeString(key)
		if err != nil || len(decoded) != sha256.Size || binding.AuthID == "" || binding.ExpiresAt.IsZero() {
			return Plan{}, errors.New("invalid_routing_state")
		}
	}
	for key, id := range state.Last {
		decoded, err := hex.DecodeString(key)
		if err != nil || len(decoded) != sha256.Size || id == "" {
			return Plan{}, errors.New("invalid_routing_state")
		}
	}
	p.loaded = state.Bindings
	p.last = state.Last
	return p, nil
}

func (r *Router) Configure(p Plan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.config.Enabled && r.loadedFile != p.config.StateFile {
		r.bindings = p.loaded
		r.loadedFile = p.config.StateFile
		if r.bindings == nil {
			r.bindings = map[string]sessionBinding{}
		}
		r.last = p.last
		if r.last == nil {
			r.last = map[string]string{}
		}
	}
	r.config = p.config
	r.generation++
}

func reject(code string) Response {
	return Response{Handled: true, Reject: true, RejectCode: code, RejectReason: "Codex quota routing: " + code}
}

func (r *Router) Pick(req Request) Response {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.config.Enabled {
		return Response{}
	}
	// Explicit host pinning (including wake requests) always takes precedence.
	if pin, _ := req.Options.Metadata["pinned_auth_id"].(string); pin != "" {
		for _, c := range req.Candidates {
			if c.ID == pin {
				return Response{Handled: true, AuthID: pin}
			}
		}
		return reject("pinned_auth_unavailable")
	}
	key := downstreamKey(req.Options.Headers)
	var rule *Binding
	for i := range r.config.Bindings {
		if r.config.Bindings[i].Key == key {
			rule = &r.config.Bindings[i]
			break
		}
	}
	// A missing header must never bypass explicitly configured key isolation.
	if (key == "" && len(r.config.Bindings) > 0) || (rule == nil && r.config.Unbound == "deny") {
		return reject("key_not_bound")
	}
	pool := make([]Candidate, 0, len(req.Candidates))
	allCodex := true
	for _, c := range req.Candidates {
		if c.ID == "" || !usable(c.Status) || (rule != nil && !matches(rule.Allow, c.ID) && !matches(rule.FallbackAllow, c.ID)) {
			continue
		}
		pool = append(pool, c)
		allCodex = allCodex && strings.EqualFold(c.Provider, "codex")
	}
	if len(pool) == 0 {
		if rule != nil {
			return reject("bound_auth_unavailable")
		}
		return Response{}
	}
	if !allCodex && rule == nil {
		return Response{}
	}
	identity := sessionID(req.Options)
	now := r.now()
	providerScope := "codex"
	if !allCodex {
		providers := map[string]bool{}
		for _, c := range pool {
			providers[strings.ToLower(c.Provider)] = true
		}
		names := make([]string, 0, len(providers))
		for provider := range providers {
			names = append(names, provider)
		}
		sort.Strings(names)
		providerScope = strings.Join(names, ",")
	}
	scope := digest(key + "\x00" + providerScope + "\x00" + strings.ToLower(strings.TrimSpace(req.Model)))
	bindingKey := digest(scope + "\x00" + identity)
	if allCodex {
		binding, bound := r.bindings[bindingKey]
		bound = identity != "" && bound && now.Before(binding.ExpiresAt)
		generation := r.generation
		// Active sessions refresh expired observations too. They never wait for
		// an unrelated refresh already in progress; that refresh will inform the
		// next turn instead of blocking a working session.
		r.mu.Unlock()
		r.refresh(pool, now, !bound)
		r.mu.Lock()
		if generation != r.generation {
			// Re-evaluate key isolation and candidates against the new configuration.
			r.mu.Unlock()
			decision := r.Pick(req)
			r.mu.Lock()
			return decision
		}
		eligible := pool[:0]
		for _, c := range pool {
			if r.quotaAvailable(c.ID, now) {
				eligible = append(eligible, c)
			}
		}
		if len(eligible) == 0 {
			return reject("quota_exhausted")
		}
		pool = eligible
	}
	// Backups are an explicitly authorized pool, not ordinary peers of healthy
	// primary accounts. A session already moved to a backup keeps that pool so a
	// primary recovery does not immediately bounce it back to the old account.
	primary, fallback := pool, []Candidate(nil)
	if rule != nil {
		primary = make([]Candidate, 0, len(pool))
		for _, c := range pool {
			if matches(rule.Allow, c.ID) {
				primary = append(primary, c)
			} else {
				fallback = append(fallback, c)
			}
		}
	}
	if identity != "" {
		stickyPool := primary
		for _, c := range fallback {
			if c.ID == r.bindings[bindingKey].AuthID {
				stickyPool = fallback
				break
			}
		}
		if id := r.reuse(bindingKey, stickyPool, now); id != "" {
			return Response{Handled: true, AuthID: id}
		}
	}
	pool = primary
	selectionReason := "weekly_then_five_hour_balance"
	if len(pool) == 0 {
		pool = fallback
		selectionReason = "bound_pool_fallback"
	}
	// Honor credential priorities after excluding exhausted quota, so lower tiers
	// can take over. Balance weekly consumption first, then short-term headroom;
	// nearby observations still rotate to spread concurrent new sessions.
	priority := pool[0].Priority
	for _, c := range pool {
		if c.Priority > priority {
			priority = c.Priority
		}
	}
	eligible := pool[:0]
	for _, c := range pool {
		if c.Priority == priority {
			eligible = append(eligible, c)
		}
	}
	pool = eligible
	if allCodex {
		pool = r.balanced(pool, now, quotaBalanceTolerance, quotaBalanceTolerance)
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
	index := sort.Search(len(pool), func(i int) bool { return pool[i].ID > r.last[scope] })
	if index == len(pool) {
		index = 0
	}
	picked := pool[index].ID
	if identity != "" {
		r.prune(now)
		if _, exists := r.bindings[bindingKey]; !exists && len(r.bindings) >= 4096 {
			// Do not evict an active session and silently move it on its next turn.
			return reject("routing_session_capacity")
		}
	}
	if len(r.last) >= 4096 {
		r.last = map[string]string{}
	}
	previousLast, hadLast := r.last[scope]
	r.last[scope] = picked
	if identity != "" {
		previous, existed := r.bindings[bindingKey]
		r.bindings[bindingKey] = sessionBinding{AuthID: picked, ExpiresAt: now.Add(r.config.sessionTTL)}
		if err := r.save(now); err != nil {
			if existed {
				r.bindings[bindingKey] = previous
			} else {
				delete(r.bindings, bindingKey)
			}
			if hadLast {
				r.last[scope] = previousLast
			} else {
				delete(r.last, scope)
			}
			return reject("routing_state_write_failed")
		}
	}
	r.host.Log("info", "quota_routing_new_binding", map[string]any{"credential": picked, "model": req.Model, "reason": selectionReason})
	return Response{Handled: true, AuthID: picked}
}

// A confirmed exhausted window stays blocked until its reset, even if its cache
// TTL expires or a refresh is in progress. Unknown and reset windows remain usable.
func (r *Router) quotaAvailable(id string, now time.Time) bool {
	q := r.quotas[id]
	return !(q.Known && now.Before(q.ResetAt) && q.UsedPercent >= 100) &&
		!(q.Weekly != nil && now.Before(q.Weekly.ResetAt) && q.Weekly.UsedPercent >= 100)
}

// balanced preserves candidates with unknown windows rather than interpreting
// missing usage as zero. A weekly-only plan can compete on its actual limit.
func (r *Router) balanced(pool []Candidate, now time.Time, weeklyTolerance, fiveHourTolerance float64) []Candidate {
	for _, window := range []struct {
		weekly    bool
		tolerance float64
	}{{true, weeklyTolerance}, {false, fiveHourTolerance}} {
		best := 101.0
		usage := func(id string) (float64, bool) {
			q := r.quotas[id]
			if now.Sub(q.ObservedAt) >= r.config.quotaTTL {
				return 0, false
			}
			if window.weekly {
				if q.Weekly != nil && now.Before(q.Weekly.ResetAt) {
					return q.Weekly.UsedPercent, true
				}
			} else if q.Known && now.Before(q.ResetAt) {
				return q.UsedPercent, true
			}
			return 0, false
		}
		for _, c := range pool {
			if used, known := usage(c.ID); known && used < best {
				best = used
			}
		}
		eligible := pool[:0]
		for _, c := range pool {
			if used, known := usage(c.ID); !known || used <= best+window.tolerance {
				eligible = append(eligible, c)
			}
		}
		pool = eligible
	}
	return pool
}

func (r *Router) refresh(pool []Candidate, now time.Time, wait bool) {
	if wait {
		r.refreshMu.Lock()
	} else if !r.refreshMu.TryLock() {
		return
	}
	defer r.refreshMu.Unlock()
	r.mu.Lock()
	ttl := r.config.quotaTTL
	needed := []Candidate{}
	for _, c := range pool {
		q, queried := r.quotas[c.ID], r.queried[c.ID]
		if queried.IsZero() || now.Sub(queried) >= ttl || (q.Known && !now.Before(q.ResetAt)) ||
			(q.Weekly != nil && !now.Before(q.Weekly.ResetAt)) {
			needed = append(needed, c)
		}
	}
	r.mu.Unlock()
	if len(needed) == 0 {
		return
	}
	auths, err := r.host.ListAuths()
	byID := map[string]keeper.Auth{}
	if err == nil {
		for _, a := range auths {
			if strings.EqualFold(a.Provider, "codex") && !a.Disabled {
				byID[a.ID] = a
			}
		}
	}
	fetchedQuotas := map[string]keeper.Quota{}
	for _, c := range needed {
		q := keeper.Quota{ObservedAt: now, Error: "quota_unavailable"}
		if a, ok := byID[c.ID]; ok {
			if fetched, err := r.host.FetchQuota(a, now); err == nil || fetched.Error != "" {
				q = fetched
			}
		}
		fetchedQuotas[c.ID] = q
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, q := range fetchedQuotas {
		// A failed or partial lookup does not establish recovery for a missing
		// window. Keep only prior confirmed exhaustion until reset; successful
		// observations of that window can establish earlier recovery.
		previous := r.quotas[id]
		if !q.Known && previous.Known && previous.UsedPercent >= 100 && now.Before(previous.ResetAt) {
			q.Known, q.UsedPercent, q.ResetAt = true, previous.UsedPercent, previous.ResetAt
		}
		if q.Weekly == nil && previous.Weekly != nil && previous.Weekly.UsedPercent >= 100 && now.Before(previous.Weekly.ResetAt) {
			q.Weekly = previous.Weekly
		}
		r.quotas[id], r.queried[id] = q, now
	}
	if len(r.quotas) > 4096 {
		for id, queried := range r.queried {
			if now.Sub(queried) > r.config.quotaTTL {
				delete(r.quotas, id)
				delete(r.queried, id)
			}
		}
	}
}

// reuse is called with mu held after the quota refresh attempt.
func (r *Router) reuse(key string, pool []Candidate, now time.Time) string {
	if b, ok := r.bindings[key]; ok && now.Before(b.ExpiresAt) {
		for _, c := range pool {
			if c.ID == b.AuthID {
				if strings.EqualFold(c.Provider, "codex") && !r.quotaAvailable(c.ID, now) {
					return ""
				}
				if strings.EqualFold(c.Provider, "codex") {
					peers := make([]Candidate, 0, len(pool))
					for _, peer := range pool {
						if strings.EqualFold(peer.Provider, c.Provider) && peer.Priority == c.Priority && r.quotaAvailable(peer.ID, now) {
							peers = append(peers, peer)
						}
					}
					peers = r.balanced(peers, now, weeklyRebindTolerance, fiveHourRebindTolerance)
					keep := false
					for _, peer := range peers {
						keep = keep || peer.ID == c.ID
					}
					if !keep {
						return ""
					}
				}
				b.ExpiresAt = now.Add(r.config.sessionTTL)
				r.bindings[key] = b
				if now.Sub(r.lastWrite) >= time.Minute {
					_ = r.save(now)
				}
				return b.AuthID
			}
		}
	}
	return ""
}

func (r *Router) prune(now time.Time) {
	for key, b := range r.bindings {
		if !now.Before(b.ExpiresAt) {
			delete(r.bindings, key)
		}
	}
}

func (r *Router) save(now time.Time) error {
	r.prune(now)
	raw, err := json.Marshal(diskState{Schema: 1, Bindings: r.bindings, Last: r.last})
	if err == nil {
		err = os.MkdirAll(filepath.Dir(r.config.StateFile), 0700)
	}
	if err == nil {
		var f *os.File
		f, err = os.CreateTemp(filepath.Dir(r.config.StateFile), ".routing-*.tmp")
		if err == nil {
			defer func() { _ = os.Remove(f.Name()) }()
			if err = f.Chmod(0600); err == nil {
				_, err = f.Write(raw)
			}
			if err == nil {
				err = f.Sync()
			}
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(f.Name(), r.config.StateFile)
			}
		}
	}
	if err != nil {
		r.stateError = "routing_state_write_failed"
		r.host.Log("warn", r.stateError, map[string]any{})
		return errors.New(r.stateError)
	}
	r.stateError, r.lastWrite = "", now
	return nil
}

func (r *Router) Status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(r.now())
	quotas := make(map[string]keeper.Quota, len(r.quotas))
	for id, q := range r.quotas {
		quotas[id] = q
	}
	return map[string]any{"enabled": r.config.Enabled, "strategy": "weekly-then-five-hour", "session_ttl": r.config.SessionTTL, "quota_cache_ttl": r.config.QuotaCacheTTL, "active_bindings": len(r.bindings), "state_error": r.stateError, "quotas": quotas}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func header(headers map[string][]string, names ...string) string {
	for _, name := range names {
		for key, values := range headers {
			if strings.EqualFold(key, name) {
				for _, value := range values {
					if value = strings.TrimSpace(value); value != "" {
						return value
					}
				}
			}
		}
	}
	return ""
}

func downstreamKey(headers map[string][]string) string {
	if value := header(headers, "Authorization"); value != "" {
		parts := strings.Fields(value)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1]
		}
		return ""
	}
	return header(headers, "X-API-Key", "Api-Key", "X-Goog-Api-Key")
}

func sessionID(opts Options) string {
	id := header(opts.Headers, "X-Codex-Thread-Id", "Session_id", "Session-Id", "X-Session-ID", "X-Conversation-ID")
	if id == "" {
		for _, key := range []string{"canonical_session_id", "execution_session_id", "derived_session_id"} {
			if value, ok := opts.Metadata[key].(string); ok && value != "" {
				id = value
				break
			}
		}
	}
	id = strings.TrimSpace(id)
	if len(id) > 512 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return ""
	}
	return id
}

func matches(patterns []string, id string) bool {
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, strings.ToLower(id)); ok {
			return true
		}
	}
	return false
}

func usable(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "disabled", "expired", "revoked", "invalid", "unavailable", "cooldown", "quota_exhausted", "blocked":
		return false
	}
	// CPA checks availability for this request's model before supplying candidates.
	// Its aggregate "error" can outlive a transient network failure or describe a
	// different model. Rejecting it here prevents recovery and backup failover.
	return true
}
