package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeHost struct {
	mu       sync.Mutex
	auths    []Auth
	codes    map[string][]int
	quotas   map[string][]Quota
	calls    map[string]int
	notified int
	clock    time.Time
	listErr  bool
}

func (h *fakeHost) ListAuths() ([]Auth, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listErr {
		return nil, errors.New("secret error")
	}
	return append([]Auth{}, h.auths...), nil
}
func (h *fakeHost) Execute(a Auth, _ Config) (Execution, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls[a.ID]++
	code := 500
	if len(h.codes[a.ID]) > 0 {
		code = h.codes[a.ID][0]
		h.codes[a.ID] = h.codes[a.ID][1:]
	}
	return Execution{StatusCode: code, Headers: http.Header{}}, nil
}
func (h *fakeHost) FetchQuota(a Auth, now time.Time) (Quota, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.quotas[a.ID]) == 0 {
		return Quota{}, errors.New("secret token")
	}
	q := h.quotas[a.ID][0]
	h.quotas[a.ID] = h.quotas[a.ID][1:]
	q.ObservedAt = now
	return q, nil
}
func (h *fakeHost) Log(string, string, map[string]any) {}
func (h *fakeHost) Notify(string, string, Config)      { h.mu.Lock(); h.notified++; h.mu.Unlock() }
func (h *fakeHost) now() time.Time                     { h.mu.Lock(); defer h.mu.Unlock(); return h.clock }
func (h *fakeHost) advance(t time.Time)                { h.mu.Lock(); h.clock = t; h.mu.Unlock() }
func fixture(t *testing.T) (*Engine, *fakeHost, Config) {
	t.Helper()
	h := &fakeHost{auths: []Auth{{ID: "a", Provider: "codex"}, {ID: "b", Provider: "codex"}}, codes: map[string][]int{}, quotas: map[string][]Quota{}, calls: map[string]int{}, clock: time.Date(2026, 9, 29, 7, 2, 0, 0, time.UTC)}
	e := New(h)
	e.now = h.now
	c := Defaults()
	c.Schedules = []Schedule{}
	c.StateFile = filepath.Join(t.TempDir(), "state.json")
	if err := e.Configure(c); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.active = true
	e.mu.Unlock()
	t.Cleanup(e.Stop)
	return e, h, c
}
func idle(t *testing.T, e *Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	if err := e.WaitForIdle(ctx); err != nil {
		t.Fatal(err)
	}
}
func slot(t *testing.T, e *Engine, id string) Slot {
	t.Helper()
	for _, s := range e.Status(e.now()).Slots {
		if s.AuthID == id {
			return s
		}
	}
	t.Fatal("missing slot")
	return Slot{}
}
func TestWaitResetThenThreeRetries(t *testing.T) {
	e, h, _ := fixture(t)
	start := h.now()
	reset := start.Add(13 * time.Minute)
	h.quotas["a"] = []Quota{{Known: true, ResetAt: reset}, {Known: true, ResetAt: reset}, {Known: true, ResetAt: reset}}
	_, err := e.Manual("a", start)
	if err != nil {
		t.Fatal(err)
	}
	idle(t, e)
	if s := slot(t, e, "a"); s.Status != "wait_for_reset" || !s.NextAttempt.Equal(reset) {
		t.Fatalf("%+v", s)
	}
	h.advance(reset.Add(-time.Second))
	e.Tick(h.now())
	idle(t, e)
	if slot(t, e, "a").Attempts != 1 {
		t.Fatal("early request")
	}
	for i := 0; i < 3; i++ {
		h.advance(reset.Add(time.Duration(i) * 30 * time.Second))
		e.Tick(h.now())
		idle(t, e)
	}
	s := slot(t, e, "a")
	if s.Attempts != 4 || s.Status != "failed" || h.notified != 1 {
		t.Fatalf("%+v notifications %d", s, h.notified)
	}
	h.advance(reset.Add(time.Hour))
	e.Tick(h.now())
	idle(t, e)
	if h.calls["a"] != 4 || h.notified != 1 {
		t.Fatal("repeated terminal task")
	}
}
func TestIndependentManualAndUnknownQuota(t *testing.T) {
	e, h, _ := fixture(t)
	h.codes["b"] = []int{200}
	_, err := e.Manual("", h.now())
	if err != nil {
		t.Fatal(err)
	}
	idle(t, e)
	a, b := slot(t, e, "a"), slot(t, e, "b")
	if a.Status != "retry_pending" || a.NextAttempt.Sub(h.now()) != 30*time.Second || b.Status != "request_ok_reset_unknown" {
		t.Fatalf("%+v %+v", a, b)
	}
	if _, err = e.Manual("", h.now()); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	h.codes["a"] = []int{200}
	h.advance(a.NextAttempt)
	e.Tick(h.now())
	idle(t, e)
	if slot(t, e, "a").Attempts != 2 || h.calls["b"] != 1 {
		t.Fatal("independence/manual retry")
	}
}
func TestZeroRetries(t *testing.T) {
	e, h, _ := fixture(t)
	e.cfg.MaxRetries = 0
	_, _ = e.Manual("a", h.now())
	idle(t, e)
	s := slot(t, e, "a")
	if s.Attempts != 1 || s.Status != "failed" || h.notified != 1 {
		t.Fatalf("%+v", s)
	}
}
func TestRestartAndConfigKeepsBudget(t *testing.T) {
	e, h, c := fixture(t)
	_, _ = e.Manual("a", h.now())
	idle(t, e)
	s := slot(t, e, "a")
	e.Stop()
	c.MaxRetries = 9
	re := New(h)
	re.now = h.now
	if err := re.Configure(c); err != nil {
		t.Fatal(err)
	}
	defer re.Stop()
	re.active = true
	h.advance(s.NextAttempt)
	re.Tick(h.now())
	idle(t, re)
	after := slot(t, re, "a")
	if after.Attempts != 2 || after.MaxRetries != 3 {
		t.Fatalf("%+v", after)
	}
	if err := re.Configure(c); err != nil {
		t.Fatal(err)
	}
	re.active = true
	h.advance(after.NextAttempt)
	re.Tick(h.now())
	idle(t, re)
	if slot(t, re, "a").Attempts != 3 {
		t.Fatal("reconfiguration lost task")
	}
}
func TestScheduleMergeAndCredentialRemoval(t *testing.T) {
	e, h, _ := fixture(t)
	e.cfg.Schedules = []Schedule{{"test", "15:02", true}}
	_, _ = e.Manual("a", h.now())
	idle(t, e)
	e.Tick(h.now())
	idle(t, e)
	count := 0
	for _, s := range e.Status(h.now()).Slots {
		if s.AuthID == "a" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("schedule duplicated pending task")
	}
	h.mu.Lock()
	h.auths[0].Disabled = true
	h.mu.Unlock()
	e.Tick(h.now())
	idle(t, e)
	if slot(t, e, "a").Status != "cancelled" {
		t.Fatal("disabled credential ran")
	}
}
func TestFutureResetMovesWithoutResettingBudget(t *testing.T) {
	e, h, _ := fixture(t)
	now := h.now()
	h.quotas["a"] = []Quota{{Known: true, ResetAt: now.Add(time.Minute)}, {Known: true, ResetAt: now.Add(5 * time.Hour)}}
	_, _ = e.Manual("a", now)
	idle(t, e)
	h.advance(now.Add(time.Minute))
	e.Tick(h.now())
	idle(t, e)
	s := slot(t, e, "a")
	if s.Attempts != 2 || !s.NextAttempt.Equal(now.Add(5*time.Hour)) {
		t.Fatalf("%+v", s)
	}
}
func TestSuccessOldWindow(t *testing.T) {
	e, h, _ := fixture(t)
	now := h.now()
	h.codes["a"] = []int{200, 200}
	h.quotas["a"] = []Quota{{Known: true, ResetAt: now.Add(time.Minute)}, {Known: true, ResetAt: now.Add(301 * time.Minute)}}
	_, _ = e.Manual("a", now)
	idle(t, e)
	if slot(t, e, "a").Status != "wait_for_reset" {
		t.Fatal("old window ignored")
	}
	h.advance(now.Add(time.Minute))
	e.Tick(h.now())
	idle(t, e)
	if slot(t, e, "a").Status != "fresh_window" {
		t.Fatal("new window not recognized")
	}
}
func TestQuotaParsing(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		body  string
		known bool
	}{{`{"rate_limit":{"primary_window":{"limit_window_seconds":604800,"reset_at":1800000000}}}`, false}, {`{"rate_limit":{"primary_window":{"limit_window_seconds":604800,"reset_at":1800000000},"secondary_window":{"limit_window_seconds":18000,"reset_at":1790665500,"used_percent":25}}}`, true}, {`{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":30}}}`, true}, {`{"rate_limit":{"primary_window":{"reset_at":1800000000}}}`, false}, {`bad`, false}} {
		q, err := ParseQuota([]byte(tc.body), now)
		if q.Known != tc.known || (err == nil) != tc.known {
			t.Fatalf("%s: %+v %v", tc.body, q, err)
		}
	}
}
func TestConfigAndNextRuns(t *testing.T) {
	c := Defaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Schedules = []Schedule{}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(NextRuns(c, time.Now())) != 0 {
		t.Fatal("empty schedules")
	}
	c.Schedules = []Schedule{{"a", "05:00", true}, {"b", "05:00", true}}
	if c.Validate() == nil {
		t.Fatal("duplicate time accepted")
	}
	c = Defaults()
	now := time.Date(2026, 9, 29, 15, 59, 0, 0, time.UTC)
	for _, s := range NextRuns(c, now) {
		if !s.Time.After(now) {
			t.Fatal("past next run")
		}
	}
	c.MaxRetries = -1
	if c.Validate() == nil {
		t.Fatal("negative retries")
	}
}
func TestStateMigrationAndFailure(t *testing.T) {
	e, h, c := fixture(t)
	_, _ = e.Manual("a", h.now())
	idle(t, e)
	e.Stop()
	raw, err := os.ReadFile(c.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state diskState
	if json.Unmarshal(raw, &state) != nil {
		t.Fatal("state")
	}
	state.Schema = 1
	state.Slots[0].MaxRetries = 0
	data, _ := json.Marshal(state)
	_ = os.WriteFile(c.StateFile, data, 0600)
	migrated, err := loadState(c.StateFile, 3)
	if err != nil || migrated.Slots[0].MaxRetries != 3 {
		t.Fatal(err)
	}
	_ = os.WriteFile(c.StateFile, []byte(`{"schema":999}`), 0600)
	if _, err = loadState(c.StateFile, 3); err == nil {
		t.Fatal("unknown schema accepted")
	}
}
