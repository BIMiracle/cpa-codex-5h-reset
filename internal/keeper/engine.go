package keeper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Auth struct {
	ID        string `json:"id"`
	AuthIndex string `json:"auth_index"`
	Label     string `json:"label"`
	Provider  string `json:"provider"`
	Disabled  bool   `json:"disabled"`
}
type Execution struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
}
type Host interface {
	ListAuths() ([]Auth, error)
	Execute(Auth, Config) (Execution, error)
	FetchQuota(Auth, time.Time) (Quota, error)
	Log(string, string, map[string]any)
	Notify(string, string, Config)
}
type Slot struct {
	Key         string    `json:"key"`
	AuthID      string    `json:"auth_id"`
	Source      string    `json:"source"`
	Due         time.Time `json:"due"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	MaxRetries  int       `json:"max_retries"`
	NextAttempt time.Time `json:"next_attempt"`
	Quota       Quota     `json:"quota"`
	StatusCode  int       `json:"status_code"`
	LastError   string    `json:"last_error,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
}

func (s Slot) Complete() bool { return !s.CompletedAt.IsZero() }

type LogEntry struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"`
	AuthID string    `json:"auth_id,omitempty"`
	TaskID string    `json:"task_id,omitempty"`
	Status string    `json:"status,omitempty"`
}
type Status struct {
	Enabled    bool            `json:"enabled"`
	Config     Config          `json:"config"`
	Now        time.Time       `json:"now"`
	Slots      []Slot          `json:"slots"`
	Auths      []Auth          `json:"auths"`
	Schedules  []ScheduledTime `json:"schedules"`
	StateError string          `json:"state_error,omitempty"`
	AuthError  string          `json:"auth_error,omitempty"`
}
type diskState struct {
	Schema int               `json:"schema"`
	Slots  []*Slot           `json:"slots"`
	Seen   map[string]string `json:"seen"`
	Logs   []LogEntry        `json:"logs"`
}

var ErrConflict = errors.New("account already has an unfinished task")
var ErrDisabled = errors.New("plugin is disabled")
var ErrNoAuth = errors.New("no matching enabled Codex credentials")

type Engine struct {
	life       sync.Mutex
	tickMu     sync.Mutex
	mu         sync.Mutex
	host       Host
	cfg        Config
	slots      map[string]*Slot
	running    map[string]bool
	seen       map[string]string
	logs       []LogEntry
	auths      []Auth
	stateError string
	authError  string
	active     bool
	stop       chan struct{}
	done       chan struct{}
	workers    sync.WaitGroup
	lastTick   time.Time
	now        func() time.Time
}

func New(h Host) *Engine {
	return &Engine{host: h, slots: map[string]*Slot{}, running: map[string]bool{}, seen: map[string]string{}, now: time.Now}
}
func (e *Engine) Configure(c Config) error {
	e.life.Lock()
	defer e.life.Unlock()
	if err := c.Validate(); err != nil {
		return err
	}
	// Validate disk input before stopping a working instance.
	e.mu.Lock()
	changed := e.cfg.StateFile != c.StateFile
	e.mu.Unlock()
	var state diskState
	var err error
	if changed {
		state, err = loadState(c.StateFile, c.MaxRetries)
		if err != nil {
			return err
		}
	}
	e.stopLocked()
	e.mu.Lock()
	defer e.mu.Unlock()
	if changed {
		e.slots = map[string]*Slot{}
		for _, s := range state.Slots {
			e.slots[s.Key] = s
		}
		e.seen = state.Seen
		e.logs = state.Logs
	}
	e.cfg = c.Clone()
	e.lastTick = e.now().Truncate(time.Minute)
	e.active = c.Enabled
	if c.Enabled {
		e.stop = make(chan struct{})
		e.done = make(chan struct{})
		go e.loop(e.stop, e.done)
	}
	return nil
}
func (e *Engine) Stop() { e.life.Lock(); defer e.life.Unlock(); e.stopLocked() }
func (e *Engine) stopLocked() {
	e.mu.Lock()
	e.active = false
	stop, done := e.stop, e.done
	e.stop = nil
	e.done = nil
	e.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	e.workers.Wait()
}
func (e *Engine) loop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			e.Tick(e.now())
		}
	}
}
func eligible(a Auth, c Config) bool {
	return strings.EqualFold(a.Provider, "codex") && !a.Disabled && a.ID != "" && c.Selects(a.ID)
}
func (e *Engine) pendingLocked(id string) *Slot {
	for _, s := range e.slots {
		if s.AuthID == id && !s.Complete() {
			return s
		}
	}
	return nil
}
func (e *Engine) newLocked(id, source string, due time.Time) *Slot {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	s := &Slot{Key: hex.EncodeToString(b), AuthID: id, Source: source, Due: due, Status: "pending", MaxRetries: e.cfg.MaxRetries, NextAttempt: due}
	e.slots[s.Key] = s
	return s
}
func (e *Engine) Tick(now time.Time) {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	e.mu.Lock()
	active := e.active
	e.mu.Unlock()
	if !active {
		return
	}
	auths, err := e.host.ListAuths()
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.active {
		return
	}
	if err != nil {
		e.authError = "auth_list_failed"
		return
	}
	e.authError = ""
	e.auths = auths
	c := e.cfg
	byID := map[string]Auth{}
	dirty := false
	for _, a := range auths {
		if eligible(a, c) {
			byID[a.ID] = a
		}
	}
	for _, s := range e.slots {
		if !s.Complete() && !e.running[s.AuthID] {
			if _, ok := byID[s.AuthID]; !ok {
				s.Status = "cancelled"
				s.LastError = "credential_removed_disabled_or_unselected"
				s.CompletedAt = now
				dirty = true
				e.logLocked("task_cancelled", s, now)
			}
		}
	}
	// Only catch up the preceding 24 hours while running; startup resumes persisted jobs.
	from := e.lastTick
	if now.Sub(from) > 24*time.Hour {
		from = now.Add(-24 * time.Hour)
	}
	loc, _ := time.LoadLocation(c.Timezone)
	local := now.In(loc)
	for _, schedule := range c.Schedules {
		if !schedule.Enabled {
			continue
		}
		clock, _ := time.Parse("15:04", schedule.At)
		for day := -1; day <= 0; day++ {
			due := time.Date(local.Year(), local.Month(), local.Day()+day, clock.Hour(), clock.Minute(), 0, 0, loc)
			if due.Before(from) || due.After(now) {
				continue
			}
			for id := range byID {
				key := id + "|" + schedule.ID
				stamp := due.Format(time.RFC3339)
				if e.seen[key] == stamp {
					continue
				}
				e.seen[key] = stamp
				dirty = true
				if e.pendingLocked(id) == nil {
					e.newLocked(id, "scheduled", due)
				}
			}
		}
	}
	e.lastTick = now
	if (dirty || e.stateError != "") && e.saveLocked() != nil {
		return
	}
	for _, s := range e.slots {
		if s.Complete() || e.running[s.AuthID] || now.Before(s.NextAttempt) {
			continue
		}
		a, ok := byID[s.AuthID]
		if !ok {
			continue
		}
		e.launchLocked(a, s, now)
	}
}
func (e *Engine) Manual(id string, now time.Time) ([]string, error) {
	e.life.Lock()
	defer e.life.Unlock()
	auths, err := e.host.ListAuths()
	if err != nil {
		return nil, errors.New("auth_list_failed")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.active {
		return nil, ErrDisabled
	}
	selected := []Auth{}
	for _, a := range auths {
		if eligible(a, e.cfg) && (id == "" || a.ID == id) {
			if e.pendingLocked(a.ID) != nil {
				return nil, ErrConflict
			}
			selected = append(selected, a)
		}
	}
	if len(selected) == 0 {
		return nil, ErrNoAuth
	}
	keys := []string{}
	e.auths = auths
	for _, a := range selected {
		s := e.newLocked(a.ID, "manual", now)
		keys = append(keys, s.Key)
	}
	if err := e.saveLocked(); err != nil {
		for _, k := range keys {
			delete(e.slots, k)
		}
		return nil, errors.New("state_write_failed")
	}
	for i, a := range selected {
		e.launchLocked(a, e.slots[keys[i]], now)
	}
	return keys, nil
}
func (e *Engine) launchLocked(a Auth, s *Slot, now time.Time) {
	if s.Attempts >= 1+s.MaxRetries {
		e.finishLocked(s, "failed", "retry_limit", now)
		e.logLocked("task_failed", s, now)
		_ = e.saveLocked()
		cfg := e.cfg.Clone()
		e.workers.Add(1)
		go func() { defer e.workers.Done(); e.host.Notify(a.ID, "retry_limit", cfg) }()
		return
	}
	e.running[a.ID] = true
	s.Attempts++
	s.Status = "requesting"
	if e.saveLocked() != nil {
		s.Attempts--
		s.Status = "pending"
		delete(e.running, a.ID)
		return
	}
	c := e.cfg.Clone()
	e.workers.Add(1)
	go e.attempt(a, s.Key, c)
}
func (e *Engine) finishLocked(s *Slot, status, reason string, now time.Time) {
	s.Status = status
	s.LastError = reason
	s.CompletedAt = now
	s.NextAttempt = time.Time{}
}
func (e *Engine) attempt(a Auth, key string, c Config) {
	defer e.workers.Done()
	result, err := e.host.Execute(a, c)
	now := e.now()
	success := err == nil && result.StatusCode >= 200 && result.StatusCode < 300
	// Query quota after every attempt, including failures; never rely on a stale window.
	q, qerr := e.host.FetchQuota(a, now)
	if qerr != nil {
		q = Quota{ObservedAt: now, Error: "quota_unavailable"}
	}
	reset, hasReset := q.ResetAt, q.Known
	if !hasReset && success {
		reset, hasReset = ResetAt(result.Headers, now)
	}
	e.mu.Lock()
	s := e.slots[key]
	s.Quota = q
	s.StatusCode = result.StatusCode
	s.LastError = ""
	notify := false
	if success && (!hasReset || !reset.After(now)) {
		e.finishLocked(s, "request_ok_reset_unknown", "", now)
	} else if success && reset.Sub(now) >= 285*time.Minute {
		e.finishLocked(s, "fresh_window", "", now)
	} else {
		if !success {
			s.LastError = fmt.Sprintf("http_%d", result.StatusCode)
			if result.StatusCode == 0 {
				s.LastError = "host_callback_failed"
			}
		}
		if s.Attempts >= 1+s.MaxRetries {
			reason := s.LastError
			if reason == "" {
				reason = "window_not_activated"
			}
			e.finishLocked(s, "failed", reason, now)
			notify = true
		} else {
			s.NextAttempt = now.Add(30 * time.Second)
			s.Status = "retry_pending"
			if hasReset && reset.After(now) {
				s.NextAttempt = reset
				s.Status = "wait_for_reset"
			}
		}
	}
	e.logLocked(s.Status, s, now)
	_ = e.saveLocked()
	delete(e.running, a.ID)
	status := s.Status
	reason := s.LastError
	e.mu.Unlock()
	e.host.Log("info", "wake_result", map[string]any{"auth_id": a.ID, "task_id": key, "status": status, "http_status": result.StatusCode})
	if notify {
		e.host.Notify(a.ID, reason, c)
	}
}
func (e *Engine) logLocked(event string, s *Slot, now time.Time) {
	e.logs = append(e.logs, LogEntry{now, event, s.AuthID, s.Key, s.Status})
	if len(e.logs) > e.cfg.MaxLogEntries {
		e.logs = e.logs[len(e.logs)-e.cfg.MaxLogEntries:]
	}
}
func (e *Engine) Status(now time.Time) Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Status{Enabled: e.active, Config: e.cfg.Clone(), Now: now, Auths: append([]Auth{}, e.auths...), Slots: []Slot{}, Schedules: NextRuns(e.cfg, now), StateError: e.stateError, AuthError: e.authError}
	for _, s := range e.slots {
		out.Slots = append(out.Slots, *s)
	}
	sort.Slice(out.Slots, func(i, j int) bool { return out.Slots[i].Due.After(out.Slots[j].Due) })
	return out
}
func (e *Engine) Logs() []LogEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]LogEntry{}, e.logs...)
}
func (e *Engine) saveLocked() error {
	state := diskState{Schema: 2, Seen: e.seen, Logs: e.logs, Slots: []*Slot{}}
	cutoff := e.now().Add(-7 * 24 * time.Hour)
	for key, s := range e.slots {
		if s.Complete() && s.CompletedAt.Before(cutoff) {
			delete(e.slots, key)
			continue
		}
		state.Slots = append(state.Slots, s)
	}
	err := writeState(e.cfg.StateFile, state)
	e.stateError = ""
	if err != nil {
		e.stateError = "state_write_failed"
	}
	return err
}
func writeState(path string, state diskState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".5h-reset-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func loadState(path string, maxRetries int) (diskState, error) {
	state := diskState{Schema: 2, Seen: map[string]string{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, errors.New("state_read_failed")
	}
	if json.Unmarshal(raw, &state) != nil {
		return state, errors.New("invalid state JSON")
	}
	if state.Schema != 1 && state.Schema != 2 {
		return state, errors.New("unsupported state schema")
	}
	if state.Seen == nil {
		state.Seen = map[string]string{}
	}
	active := map[string]bool{}
	for _, s := range state.Slots {
		if s == nil || s.Key == "" || s.AuthID == "" {
			return state, errors.New("invalid state task")
		}
		if state.Schema == 1 {
			s.MaxRetries = maxRetries
		}
		if !s.Complete() {
			if active[s.AuthID] {
				s.Status = "cancelled"
				s.CompletedAt = time.Now()
				s.LastError = "duplicate_legacy_task"
			} else {
				active[s.AuthID] = true
				if s.Status == "requesting" {
					s.Status = "retry_pending"
				}
			}
		}
	}
	state.Schema = 2
	return state, nil
}
func (e *Engine) WaitForIdle(ctx context.Context) error {
	for {
		e.mu.Lock()
		idle := len(e.running) == 0
		e.mu.Unlock()
		if idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}
