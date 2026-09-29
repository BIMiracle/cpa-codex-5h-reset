package keeper

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestResetAtAndWindowDecision(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 2, 0, 0, time.FixedZone("SGT", 8*3600))
	due := now
	deadline := now.Add(3 * time.Hour)
	headers := http.Header{}
	headers.Set("X-Codex-Primary-Reset-At", "1790665500")
	reset, found := ResetAt(headers, now)
	if !found || reset.Unix() != 1790665500 {
		t.Fatalf("absolute reset header: %v, %v", reset, found)
	}
	reset = now.Add(13 * time.Minute)
	if got := DecideWindow(reset, true, now, due, deadline); got != WindowWait {
		t.Fatalf("old window with imminent reset: %s", got)
	}
	if got := DecideWindow(now.Add(5*time.Hour), true, now, due, deadline); got != WindowFresh {
		t.Fatalf("new window: %s", got)
	}
	if got := DecideWindow(now.Add(4*time.Hour), true, now, due, deadline); got != WindowAlreadyOpen {
		t.Fatalf("old window beyond deadline: %s", got)
	}
	if got := DecideWindow(time.Time{}, false, now, due, deadline); got != WindowUnknown {
		t.Fatalf("missing reset signal: %s", got)
	}
}

type fakeHost struct {
	mu       sync.Mutex
	auths    []Auth
	results  []Execution
	byID     map[string][]Execution
	notified int
}

func (h *fakeHost) ListAuths() ([]Auth, error) { return h.auths, nil }
func (h *fakeHost) Execute(auth Auth, _ Config) (Execution, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byID != nil {
		results := h.byID[auth.ID]
		if len(results) == 0 {
			return Execution{}, errors.New("unexpected credential request")
		}
		h.byID[auth.ID] = results[1:]
		return results[0], nil
	}
	if len(h.results) == 0 {
		return Execution{}, errors.New("unexpected extra request")
	}
	result := h.results[0]
	h.results = h.results[1:]
	return result, nil
}

func TestAccountsProgressIndependently(t *testing.T) {
	now := time.Now()
	cfg := Defaults()
	cfg.Enabled = true
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	cfg.Times = []string{now.In(mustLocation(t, cfg.Timezone)).Format("15:04")}
	cfg.RetryInitialSeconds = 1
	cfg.RetryMaxSeconds = 2
	cfg.MaxDelayMinutes = 10
	fresh := Execution{StatusCode: 200, Headers: http.Header{
		"X-Codex-Primary-Reset-At": []string{strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10)},
	}}
	h := &fakeHost{
		auths: []Auth{{ID: "a", Provider: "codex"}, {ID: "b", Provider: "codex"}},
		byID: map[string][]Execution{
			"a": {{StatusCode: 500}, fresh},
			"b": {fresh},
		},
	}
	e := New(h)
	if err := e.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	e.Tick(now)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.WaitForIdle(ctx); err != nil {
		t.Fatal(err)
	}
	byAuth := map[string]Slot{}
	for _, slot := range e.Status(time.Now()).Slots {
		byAuth[slot.AuthID] = slot
	}
	if byAuth["a"].Status != "retry_pending" || byAuth["b"].Status != string(WindowFresh) {
		t.Fatalf("account states after first attempt: %+v", byAuth)
	}
	e.Tick(byAuth["a"].NextAttempt.Add(time.Millisecond))
	if err := e.WaitForIdle(ctx); err != nil {
		t.Fatal(err)
	}
	byAuth = map[string]Slot{}
	for _, slot := range e.Status(time.Now()).Slots {
		byAuth[slot.AuthID] = slot
	}
	if byAuth["a"].Status != string(WindowFresh) || byAuth["b"].Attempts != 1 {
		t.Fatalf("account states after retry: %+v", byAuth)
	}
}
func (h *fakeHost) Log(_, _ string, _ map[string]any) {}
func (h *fakeHost) Notify(_, _ string, _ Config) {
	h.mu.Lock()
	h.notified++
	h.mu.Unlock()
}

func TestPerCredentialHTTP500RetryAndPersistence(t *testing.T) {
	now := time.Now()
	cfg := Defaults()
	cfg.Enabled = true
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	cfg.RetryInitialSeconds = 1
	cfg.RetryMaxSeconds = 2
	cfg.MaxDelayMinutes = 10
	cfg.Times = []string{now.In(mustLocation(t, cfg.Timezone)).Format("15:04")}
	h := &fakeHost{
		auths: []Auth{{ID: "account-a", Provider: "codex"}},
		results: []Execution{
			{StatusCode: 500},
			{StatusCode: 200, Headers: http.Header{}},
		},
	}
	// The response header is a Unix timestamp; use an actual epoch value.
	h.results[1].Headers.Set("X-Codex-Primary-Reset-At",
		strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10))
	e := New(h)
	if err := e.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	e.Tick(now)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.WaitForIdle(ctx); err != nil {
		t.Fatal(err)
	}
	slots := e.Status(time.Now()).Slots
	if len(slots) != 1 || slots[0].Status != "retry_pending" || slots[0].Attempts != 1 {
		t.Fatalf("after 500: %+v", slots)
	}
	// Advance the scheduler clock past its next attempt without sleeping.
	e.Tick(slots[0].NextAttempt.Add(time.Millisecond))
	if err := e.WaitForIdle(ctx); err != nil {
		t.Fatal(err)
	}
	slots = e.Status(time.Now()).Slots
	if slots[0].Status != string(WindowFresh) || slots[0].Attempts != 2 || !slots[0].Complete() {
		t.Fatalf("after retry: %+v", slots[0])
	}
	h.mu.Lock()
	notified := h.notified
	h.mu.Unlock()
	if notified != 0 {
		t.Fatalf("unexpected notification count %d", notified)
	}
	stored, err := loadState(cfg.StateFile)
	if err != nil || len(stored) != 1 {
		t.Fatalf("persistent state: %v, %v", stored, err)
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
