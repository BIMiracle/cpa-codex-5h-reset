package keeper

import (
	"context"
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
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Disabled bool   `json:"disabled"`
}

type Execution struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
}

type Host interface {
	ListAuths() ([]Auth, error)
	Execute(Auth, Config) (Execution, error)
	Log(level, event string, fields map[string]any)
	Notify(authID, reason string, cfg Config)
}

type Slot struct {
	Key         string    `json:"key"`
	AuthID      string    `json:"auth_id"`
	Due         time.Time `json:"due"`
	Deadline    time.Time `json:"deadline"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt,omitempty"`
	ResetAt     time.Time `json:"reset_at,omitempty"`
	StatusCode  int       `json:"status_code,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

func (s Slot) Complete() bool { return !s.CompletedAt.IsZero() }

type Status struct {
	Enabled  bool      `json:"enabled"`
	Model    string    `json:"model"`
	Timezone string    `json:"timezone"`
	Times    []string  `json:"times"`
	Now      time.Time `json:"now"`
	Slots    []Slot    `json:"slots"`
}

type diskState struct {
	Schema int    `json:"schema"`
	Slots  []Slot `json:"slots"`
}

type Engine struct {
	mu      sync.Mutex
	host    Host
	cfg     Config
	slots   map[string]*Slot
	running map[string]bool
	stop    chan struct{}
	done    chan struct{}
	workers sync.WaitGroup
}

func New(host Host) *Engine {
	return &Engine{host: host, slots: make(map[string]*Slot), running: make(map[string]bool)}
}

func (e *Engine) Configure(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	e.Stop()
	e.mu.Lock()
	if e.cfg.StateFile != cfg.StateFile {
		slots, err := loadState(cfg.StateFile)
		if err != nil {
			e.mu.Unlock()
			return err
		}
		e.slots = slots
	}
	e.cfg = cfg
	e.running = make(map[string]bool)
	if cfg.Enabled {
		e.stop = make(chan struct{})
		e.done = make(chan struct{})
	}
	stop, done := e.stop, e.done
	e.mu.Unlock()
	if cfg.Enabled {
		go e.loop(stop, done)
	}
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	stop, done := e.stop, e.done
	e.stop, e.done = nil, nil
	e.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	e.workers.Wait()
}

func (e *Engine) loop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	e.Tick(time.Now())
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			e.Tick(now)
		}
	}
}

func slotKey(authID string, due time.Time) string {
	return fmt.Sprintf("%s|%d", authID, due.Unix())
}

// Tick is exported so the scheduler can be tested with a fake clock and host.
func (e *Engine) Tick(now time.Time) {
	e.mu.Lock()
	cfg := e.cfg
	enabled := cfg.Enabled && e.stop != nil
	e.mu.Unlock()
	if !enabled {
		return
	}
	auths, err := e.host.ListAuths()
	if err != nil {
		e.host.Log("warn", "auth_list_failed", map[string]any{"error_type": "host_callback"})
		return
	}
	for _, auth := range auths {
		if !strings.EqualFold(auth.Provider, "codex") || auth.Disabled || auth.ID == "" {
			continue
		}
		for _, due := range DueTimes(cfg, now) {
			deadline := due.Add(time.Duration(cfg.MaxDelayMinutes) * time.Minute)
			if now.Before(due) || now.After(deadline) {
				continue
			}
			e.queue(auth, due, deadline, now)
		}
	}
}

func (e *Engine) queue(auth Auth, due, deadline, now time.Time) {
	key := slotKey(auth.ID, due)
	e.mu.Lock()
	slot := e.slots[key]
	if slot == nil {
		slot = &Slot{Key: key, AuthID: auth.ID, Due: due, Deadline: deadline, Status: "pending", NextAttempt: now}
		e.slots[key] = slot
		e.saveOrLogLocked()
	}
	if e.stop == nil || slot.Complete() || e.running[key] || now.Before(slot.NextAttempt) {
		e.mu.Unlock()
		return
	}
	e.running[key] = true
	e.workers.Add(1)
	cfg := e.cfg
	e.mu.Unlock()
	go e.attempt(auth, key, cfg)
}

func (e *Engine) Manual(authID string, now time.Time) ([]string, error) {
	auths, err := e.host.ListAuths()
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	cfg := e.cfg
	enabled := cfg.Enabled && e.stop != nil
	e.mu.Unlock()
	if !enabled {
		return nil, errors.New("plugin is disabled")
	}
	var keys []string
	for _, auth := range auths {
		if !strings.EqualFold(auth.Provider, "codex") || auth.Disabled || auth.ID == "" ||
			(authID != "" && auth.ID != authID) {
			continue
		}
		due := now.Add(time.Duration(len(keys)) * time.Nanosecond)
		e.queue(auth, due, due.Add(time.Duration(cfg.MaxDelayMinutes)*time.Minute), now)
		keys = append(keys, slotKey(auth.ID, due))
	}
	if len(keys) == 0 {
		return nil, errors.New("no matching active Codex credentials")
	}
	return keys, nil
}

func (e *Engine) attempt(auth Auth, key string, cfg Config) {
	defer e.workers.Done()
	e.mu.Lock()
	slot := e.slots[key]
	if slot == nil {
		delete(e.running, key)
		e.mu.Unlock()
		return
	}
	slot.Attempts++
	attempt := slot.Attempts
	slot.Status = "requesting"
	e.saveOrLogLocked()
	e.mu.Unlock()

	result, requestErr := e.host.Execute(auth, cfg)
	now := time.Now()
	e.mu.Lock()
	slot = e.slots[key]
	if slot == nil {
		delete(e.running, key)
		e.mu.Unlock()
		return
	}
	slot.StatusCode = result.StatusCode
	slot.LastError = ""
	level, event := "info", "slot_complete"
	notify := false
	if requestErr == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
		resetAt, found := ResetAt(result.Headers, now)
		if found {
			slot.ResetAt = resetAt
		}
		switch DecideWindow(resetAt, found, now, slot.Due, slot.Deadline) {
		case WindowWait:
			slot.Status = string(WindowWait)
			slot.NextAttempt = resetAt.Add(time.Duration(cfg.GraceSeconds) * time.Second)
			if slot.NextAttempt.After(slot.Deadline) {
				slot.Status = string(WindowAlreadyOpen)
				slot.CompletedAt = now
			} else {
				event = "waiting_for_reset"
			}
		case WindowFresh:
			slot.Status = string(WindowFresh)
			slot.CompletedAt = now
		case WindowAlreadyOpen:
			slot.Status = string(WindowAlreadyOpen)
			slot.CompletedAt = now
		default:
			slot.Status = string(WindowUnknown)
			slot.CompletedAt = now
		}
	} else {
		status := result.StatusCode
		if status == 0 {
			slot.LastError = "host_callback_failed"
		} else {
			slot.LastError = fmt.Sprintf("http_%d", status)
		}
		transient := status == 0 || status == 408 || status == 409 || status == 429 || status >= 500
		delay := RetryDelay(cfg, attempt)
		next := now.Add(delay)
		if transient && attempt < cfg.RetryAttempts && !next.After(slot.Deadline) {
			slot.Status = "retry_pending"
			slot.NextAttempt = next
			level, event = "warn", "retry_scheduled"
		} else {
			slot.Status = "failed"
			slot.CompletedAt = now
			level, event, notify = "error", "slot_failed", true
		}
	}
	e.saveOrLogLocked()
	delete(e.running, key)
	fields := map[string]any{
		"auth_id": auth.ID, "due": slot.Due.Format(time.RFC3339),
		"status": slot.Status, "attempt": slot.Attempts,
		"http_status": slot.StatusCode, "next_attempt": slot.NextAttempt.Format(time.RFC3339),
	}
	if !slot.ResetAt.IsZero() {
		fields["reset_at"] = slot.ResetAt.Format(time.RFC3339)
	}
	reason := slot.LastError
	e.mu.Unlock()
	e.host.Log(level, event, fields)
	if notify {
		e.host.Notify(auth.ID, reason, cfg)
	}
}

func (e *Engine) Status(now time.Time) Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Status{
		Enabled: e.cfg.Enabled, Model: e.cfg.Model, Timezone: e.cfg.Timezone,
		Times: append([]string(nil), e.cfg.Times...), Now: now,
	}
	for _, slot := range e.slots {
		out.Slots = append(out.Slots, *slot)
	}
	sort.Slice(out.Slots, func(i, j int) bool {
		if out.Slots[i].Due.Equal(out.Slots[j].Due) {
			return out.Slots[i].AuthID < out.Slots[j].AuthID
		}
		return out.Slots[i].Due.After(out.Slots[j].Due)
	})
	if len(out.Slots) > 100 {
		out.Slots = out.Slots[:100]
	}
	return out
}

func (e *Engine) saveOrLogLocked() {
	if err := e.saveLocked(); err != nil {
		// Avoid calling the host while the engine lock is held.
		go e.host.Log("error", "state_write_failed", map[string]any{"error_type": "file_io"})
	}
}

func (e *Engine) saveLocked() error {
	if e.cfg.StateFile == "" {
		return nil
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	state := diskState{Schema: 1}
	for key, slot := range e.slots {
		if slot.Due.Before(cutoff) {
			delete(e.slots, key)
			continue
		}
		state.Slots = append(state.Slots, *slot)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(e.cfg.StateFile), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(e.cfg.StateFile), ".keeper-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(raw)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, e.cfg.StateFile)
}

func loadState(path string) (map[string]*Slot, error) {
	out := make(map[string]*Slot)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var state diskState
	if err = json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if state.Schema != 1 {
		return nil, fmt.Errorf("unsupported state schema %d", state.Schema)
	}
	for _, slot := range state.Slots {
		copy := slot
		out[slot.Key] = &copy
	}
	return out, nil
}

// WaitForIdle is useful for deterministic tests; production uses asynchronous
// workers so unrelated account attempts can run at the same time.
func (e *Engine) WaitForIdle(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
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
		case <-ticker.C:
		}
	}
}
