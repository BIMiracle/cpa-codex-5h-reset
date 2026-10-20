package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
)

type fakeHost struct {
	used  map[string]float64
	fail  bool
	calls int
	now   time.Time
}

func (h *fakeHost) ListAuths() ([]keeper.Auth, error) {
	return []keeper.Auth{{ID: "a.json", AuthIndex: "a", Provider: "codex"}, {ID: "b.json", AuthIndex: "b", Provider: "codex"}}, nil
}
func (h *fakeHost) FetchQuota(a keeper.Auth, now time.Time) (keeper.Quota, error) {
	h.calls++
	if h.fail {
		return keeper.Quota{}, errors.New("never expose secret-token")
	}
	return keeper.Quota{Known: true, UsedPercent: h.used[a.ID], ResetAt: h.now.Add(5 * time.Hour), ObservedAt: now}, nil
}
func (*fakeHost) Log(string, string, map[string]any) {}

func configured(t *testing.T, h *fakeHost, extra string) *Router {
	t.Helper()
	c, err := Parse([]byte("quota_routing:\n  enabled: true\n"+extra), filepath.Join(t.TempDir(), "wake.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := New(h)
	r.now = func() time.Time { return h.now }
	plan, err := r.Prepare(c)
	if err != nil {
		t.Fatal(err)
	}
	r.Configure(plan)
	return r
}

func request(session string) Request {
	return Request{Provider: "codex", Model: "gpt-6.1-sol", Options: Options{Headers: map[string][]string{"Authorization": {"Bearer client-key"}, "session_id": {session}}}, Candidates: []Candidate{{ID: "a.json", Provider: "codex"}, {ID: "b.json", Provider: "codex"}}}
}

func fixture() *fakeHost {
	return &fakeHost{now: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), used: map[string]float64{"a.json": 76, "b.json": 21}}
}

func picked(t *testing.T, r *Router, req Request, want string) {
	t.Helper()
	if got := r.Pick(req); !got.Handled || got.Reject || got.AuthID != want {
		t.Fatalf("pick=%+v, want %s", got, want)
	}
}

func TestNewSessionHeadroomAndExistingSessionAffinity(t *testing.T) {
	h := fixture()
	r := configured(t, h, "")
	picked(t, r, request("old-session"), "b.json")
	h.used = map[string]float64{"a.json": 10, "b.json": 80}
	h.now = h.now.Add(2 * time.Minute)
	picked(t, r, request("old-session"), "b.json")
	if h.calls != 2 {
		t.Fatal("sticky request queried upstream quota", h.calls)
	}
	picked(t, r, request("new-session"), "a.json")
	picked(t, r, request("old-session"), "b.json")
	if h.calls != 4 {
		t.Fatal("quota cache not reused", h.calls)
	}
	// Host retries remove the failed/limited auth from Candidates.
	retry := request("old-session")
	retry.Candidates = retry.Candidates[:1]
	picked(t, r, retry, "a.json")
	picked(t, r, request("old-session"), "a.json")
}

func TestBindingsSurviveRestartReconfigureAndDisable(t *testing.T) {
	h := fixture()
	r := configured(t, h, "")
	picked(t, r, request("private-session"), "b.json")
	raw, err := os.ReadFile(r.config.StateFile)
	if err != nil || strings.Contains(string(raw), "client-key") || strings.Contains(string(raw), "private-session") {
		t.Fatalf("state leaks caller identity: %s %v", raw, err)
	}
	h.used = map[string]float64{"a.json": 1, "b.json": 99}
	restarted := New(h)
	restarted.now = r.now
	// Loading a disabled plugin first must not suppress a later state restore.
	disabled := r.config
	disabled.Enabled = false
	plan, err := restarted.Prepare(disabled)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Configure(plan)
	plan, err = restarted.Prepare(r.config)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Configure(plan)
	picked(t, restarted, request("private-session"), "b.json")
	plan, _ = restarted.Prepare(r.config)
	restarted.Configure(plan)
	picked(t, restarted, request("private-session"), "b.json")
	h.now = h.now.Add(4*time.Hour + time.Second)
	picked(t, restarted, request("private-session"), "a.json")
}

func TestUnknownQuotaTieFallbackAndResetInvalidation(t *testing.T) {
	h := fixture()
	h.fail = true
	r := configured(t, h, "")
	picked(t, r, request("one"), "a.json")
	picked(t, r, request("two"), "b.json")
	if h.calls != 2 {
		t.Fatal("failed quota lookups were not cached")
	}
	h.fail = false
	h.now = h.now.Add(time.Minute)
	h.used = map[string]float64{"a.json": 30, "b.json": 30}
	picked(t, r, request("three"), "a.json")
	picked(t, r, request("four"), "b.json")
	// An imminent reset invalidates cached usage before the ordinary cache TTL.
	q := r.quotas["a.json"]
	q.ResetAt = h.now.Add(time.Second)
	r.quotas["a.json"] = q
	h.now = h.now.Add(2 * time.Second)
	h.used["a.json"] = 0
	picked(t, r, request("after-reset"), "a.json")
	if h.calls != 5 {
		t.Fatalf("expired quota was not refreshed: %d", h.calls)
	}
}

func TestKeyIsolationPinningProviderAndPriority(t *testing.T) {
	h := fixture()
	r := configured(t, h, "  bindings:\n    - key: client-key\n      allow: [a.*]\n")
	picked(t, r, request("session"), "a.json")
	bound := request("session")
	bound.Candidates = bound.Candidates[1:]
	if got := r.Pick(bound); !got.Reject || got.RejectCode != "bound_auth_unavailable" {
		t.Fatalf("bound key escaped its pool: %+v", got)
	}
	internal := request("")
	internal.Options = Options{Metadata: map[string]any{"pinned_auth_id": "b.json"}}
	picked(t, r, internal, "b.json")
	missing := request("session")
	delete(missing.Options.Headers, "Authorization")
	if got := r.Pick(missing); !got.Reject {
		t.Fatal("missing header bypassed key isolation")
	}
	otherKey := request("session")
	otherKey.Options.Headers["Authorization"] = []string{"Bearer other-key"}
	picked(t, r, otherKey, "b.json")
	denied := configured(t, h, "  unbound: deny\n")
	if got := denied.Pick(request("x")); !got.Reject {
		t.Fatal("unbound=deny bypassed")
	}
	r = configured(t, h, "")
	priority := request("important")
	priority.Candidates[0].Priority = 2
	picked(t, r, priority, "a.json")
	priority.Candidates[1].Priority = 9
	picked(t, r, priority, "a.json")
	priority.Options.Headers["session_id"] = []string{"new-priority-session"}
	picked(t, r, priority, "b.json")
	claude := request("other-provider")
	claude.Candidates[0].Provider = "claude"
	if got := r.Pick(claude); got.Handled {
		t.Fatal("unbound non-Codex/mixed-provider route was intercepted")
	}
}

func TestCallerAndModelNamespacesAndMissingIdentity(t *testing.T) {
	h := fixture()
	h.used["a.json"] = 21
	r := configured(t, h, "")
	picked(t, r, request("same-id"), "a.json")
	h.now = h.now.Add(time.Minute)
	h.used["a.json"], h.used["b.json"] = 90, 1
	other := request("same-id")
	other.Options.Headers["Authorization"] = []string{"Bearer Client-Key"}
	picked(t, r, other, "b.json")
	other = request("same-id")
	other.Model = "gpt-6-luna"
	picked(t, r, other, "b.json")
	picked(t, r, request("same-id"), "a.json")
	missing := request("")
	if got := r.Pick(missing); got.Handled {
		t.Fatal("request without stable identity was intercepted")
	}
	missing.Options.Metadata = map[string]any{"execution_session_id": "execution-identity"}
	picked(t, r, missing, "b.json")
}

func TestExhaustedQuotaAndBrokenStateFailClosed(t *testing.T) {
	h := fixture()
	h.used = map[string]float64{"a.json": 100, "b.json": 100}
	r := configured(t, h, "")
	if got := r.Pick(request("empty")); !got.Reject || got.RejectCode != "five_hour_quota_exhausted" {
		t.Fatalf("exhausted pool accepted: %+v", got)
	}
	h = fixture()
	r = configured(t, h, "")
	if err := os.MkdirAll(r.config.StateFile, 0700); err != nil {
		t.Fatal(err)
	}
	if got := r.Pick(request("cannot-save")); !got.Reject || len(r.bindings) != 0 {
		t.Fatalf("unpersisted new binding accepted: %+v", got)
	}
	status, _ := json.Marshal(r.Status())
	if strings.Contains(string(status), "client-key") || strings.Contains(string(status), "secret-token") {
		t.Fatal("status leaks credentials")
	}
	c := r.config
	c.StateFile = filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(c.StateFile, []byte(`{"schema":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Prepare(c); err == nil {
		t.Fatal("bad persisted schema accepted")
	}
}

func TestConcurrentSessionsAndBoundedCapacity(t *testing.T) {
	h := fixture()
	r := configured(t, h, "")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := r.Pick(request(fmt.Sprint(i))); got.AuthID != "b.json" || got.Reject {
				t.Errorf("concurrent pick: %+v", got)
			}
		}()
	}
	wg.Wait()
	if h.calls != 2 || len(r.bindings) != 20 {
		t.Fatalf("parallel lookup duplication/binding loss: %d %d", h.calls, len(r.bindings))
	}
	for i := len(r.bindings); i < 4096; i++ {
		r.bindings[digest(fmt.Sprint(i)+"capacity")] = sessionBinding{AuthID: "a.json", ExpiresAt: h.now.Add(time.Hour)}
	}
	if got := r.Pick(request("capacity-new")); !got.Reject || got.RejectCode != "routing_session_capacity" {
		t.Fatal("capacity silently evicted active bindings", got)
	}
	picked(t, r, request("0"), "b.json")
	retry := request("0")
	retry.Candidates = retry.Candidates[:1]
	picked(t, r, retry, "a.json")
}

func TestConfigurationValidation(t *testing.T) {
	for _, raw := range []string{
		"quota_routing: {session_ttl: 0s}",
		"quota_routing: {quota_cache_ttl: 24h}",
		"quota_routing: {unbound: allow}",
		"quota_routing: {bindings: [{key: secret-token, allow: ['[']}]}",
		"quota_routing: {bindings: [{key: secret-token, allow: []}]}",
		"quota_routing: {bindings: [{key: a, allow: ['*']}, {key: a, allow: ['*']}]}",
	} {
		_, err := Parse([]byte(raw), "state.json")
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("invalid/secret-bearing validation result: %v", err)
		}
	}
	c, err := Parse(nil, "state.json")
	if err != nil || c.Enabled || c.SessionTTL != "4h" || c.QuotaCacheTTL != "1m" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
}

type blockingHost struct {
	*fakeHost
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *blockingHost) FetchQuota(a keeper.Auth, now time.Time) (keeper.Quota, error) {
	h.once.Do(func() { close(h.started) })
	<-h.release
	return h.fakeHost.FetchQuota(a, now)
}

func TestSlowQuotaLookupDoesNotBlockExistingSession(t *testing.T) {
	h := fixture()
	r := configured(t, h, "")
	picked(t, r, request("existing"), "b.json")
	h.now = h.now.Add(2 * time.Minute)
	blocked := &blockingHost{fakeHost: h, started: make(chan struct{}), release: make(chan struct{})}
	r.host = blocked
	done := make(chan Response, 1)
	go func() { done <- r.Pick(request("new")) }()
	<-blocked.started
	sticky := make(chan Response, 1)
	go func() { sticky <- r.Pick(request("existing")) }()
	select {
	case got := <-sticky:
		if got.AuthID != "b.json" {
			t.Error(got)
		}
	case <-time.After(time.Second):
		t.Error("existing session blocked by unrelated quota request")
	}
	close(blocked.release)
	if got := <-done; got.AuthID != "b.json" {
		t.Fatal(got)
	}
}
