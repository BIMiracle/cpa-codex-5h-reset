package keeper

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func setDateConfig(e *Engine, c Config, now time.Time) {
	e.mu.Lock()
	e.cfg = c
	e.active = true
	e.lastTick = now.Add(-time.Minute)
	e.mu.Unlock()
}

func offlineCalendar(e *Engine) {
	e.holidays.fetch = func(context.Context, string) ([]byte, error) { return nil, errors.New("offline fixture") }
}

func TestTomorrowWakeAndDefaultRulesResume(t *testing.T) {
	e, h, c := fixture(t)
	offlineCalendar(e)
	c.SkipCNHolidays = true
	c.OneTimeRule = OneTimeRule{"wake_tomorrow", "2026-10-03", "2026-10-03"}
	c.Schedules = []Schedule{{"morning", "05:00", true}}
	for _, date := range []string{"2026-10-03", "2026-10-04", "2026-10-05"} {
		now := holidayTime(date, "05:00")
		h.advance(now)
		h.codes["a"], h.codes["b"] = []int{200}, []int{200}
		setDateConfig(e, c, now)
		e.Tick(now)
		idle(t, e)
		if date == "2026-10-04" {
			if s := slot(t, e, "a"); s.Status != "skipped" || s.Attempts != 0 || s.LastError != "weekend" {
				t.Fatal(s)
			}
		}
	}
	if h.calls["a"] != 2 || h.calls["b"] != 2 || h.notified != 0 {
		t.Fatalf("calls=%v notifications=%d", h.calls, h.notified)
	}
	if e.Status(h.now()).EffectiveRule.Mode != "none" {
		t.Fatal("one-time rule did not expire")
	}
}

func TestExcludeRangeNoBackfillAfterExpiration(t *testing.T) {
	e, h, c := fixture(t)
	c.OneTimeRule = OneTimeRule{"exclude", "2026-10-01", "2026-10-07"}
	c.Schedules = []Schedule{{"morning", "05:00", true}}
	blocked := holidayTime("2026-10-07", "05:00")
	h.advance(blocked)
	setDateConfig(e, c, blocked)
	e.Tick(blocked)
	idle(t, e)
	if h.calls["a"] != 0 || slot(t, e, "a").LastError != "excluded_date" {
		t.Fatal("excluded date executed")
	}
	// The preceding day is inside the engine's catch-up range, but excluded.
	resumed := holidayTime("2026-10-08", "05:00")
	h.advance(resumed)
	h.codes["a"], h.codes["b"] = []int{200}, []int{200}
	e.Tick(resumed)
	idle(t, e)
	if h.calls["a"] != 1 || h.calls["b"] != 1 {
		t.Fatal("excluded tasks were replayed", h.calls)
	}
	// Loading the saved absolute rule cannot turn it into a new interval.
	restarted := New(h)
	restarted.now = h.now
	t.Cleanup(restarted.Stop)
	if err := restarted.Configure(c); err != nil {
		t.Fatal(err)
	}
	if restarted.Status(resumed).EffectiveRule.Mode != "none" || restarted.Status(resumed).Config.OneTimeRule != c.OneTimeRule {
		t.Fatal("restart changed the one-time interval")
	}
}

func TestSkipPendingAutomaticTaskButAllowManualRetries(t *testing.T) {
	e, h, c := fixture(t)
	offlineCalendar(e)
	c.SkipCNHolidays = true
	now := holidayTime("2026-10-03", "05:00")
	h.advance(now)
	setDateConfig(e, c, now)
	e.mu.Lock()
	old := e.newLocked("a", "scheduled", holidayTime("2026-10-02", "23:59"))
	old.Status, old.NextAttempt = "wait_for_reset", now.Add(time.Hour)
	e.mu.Unlock()
	// Manual is permitted even before the next ticker has cancelled old jobs.
	h.codes["a"] = []int{500, 200}
	if _, err := e.Manual("a", now); err != nil {
		t.Fatal(err)
	}
	idle(t, e)
	if !old.Complete() || old.Status != "skipped" {
		t.Fatal("old scheduled job was not skipped", old)
	}
	h.advance(now.Add(30 * time.Second))
	e.Tick(h.now())
	idle(t, e)
	if h.calls["a"] != 2 || slot(t, e, "a").Source != "manual" || !slot(t, e, "a").Complete() || h.notified != 0 {
		t.Fatal("manual task did not bypass dates")
	}
}

func TestCatchUpChecksOriginalAndActualDate(t *testing.T) {
	for _, tc := range []struct{ from, now string }{
		{"2026-10-02 23:58", "2026-10-03 00:01"},
		{"2026-10-04 23:58", "2026-10-05 00:01"},
	} {
		t.Run(tc.now, func(t *testing.T) {
			e, h, c := fixture(t)
			offlineCalendar(e)
			c.SkipCNHolidays = true
			c.Schedules = []Schedule{{"late", "23:59", true}}
			from := holidayTime(tc.from[:10], tc.from[11:])
			now := holidayTime(tc.now[:10], tc.now[11:])
			h.advance(now)
			setDateConfig(e, c, now)
			e.mu.Lock()
			e.lastTick = from
			e.mu.Unlock()
			e.Tick(now)
			idle(t, e)
			if h.calls["a"] != 0 || slot(t, e, "a").Status != "skipped" {
				t.Fatal("catch-up bypassed date policy")
			}
		})
	}
}

type blockedDateHost struct {
	*fakeHost
	started chan struct{}
	release chan struct{}
}

func (h *blockedDateHost) Execute(a Auth, c Config) (Execution, error) {
	close(h.started)
	<-h.release
	return h.fakeHost.Execute(a, c)
}

func TestInflightAutomaticRequestFinishesWithoutForbiddenRetryOrNotification(t *testing.T) {
	fake := &fakeHost{auths: []Auth{{ID: "a", Provider: "codex", PlanType: "plus"}}, codes: map[string][]int{}, quotas: map[string][]Quota{}, calls: map[string]int{}, clock: holidayTime("2026-10-02", "23:59")}
	h := &blockedDateHost{fake, make(chan struct{}), make(chan struct{})}
	e := New(h)
	e.now = h.now
	offlineCalendar(e)
	c := Defaults()
	c.StateFile = filepath.Join(t.TempDir(), "state.json")
	c.SkipCNHolidays, c.MaxRetries = true, 0
	c.Schedules = []Schedule{{"late", "23:59", true}}
	if err := e.Configure(c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	setDateConfig(e, c, h.now())
	e.Tick(h.now())
	<-h.started
	h.advance(holidayTime("2026-10-03", "00:00"))
	close(h.release)
	idle(t, e)
	if s := slot(t, e, "a"); s.Status != "skipped" || s.Attempts != 1 || !s.Complete() {
		t.Fatal(s)
	}
	if h.calls["a"] != 1 || h.notified != 0 {
		t.Fatal("in-flight request retried or notified", h.calls, h.notified)
	}
}

func TestHolidayPreviewMatchesExecutionAndImportPreviewDoesNotWrite(t *testing.T) {
	e, h, c := fixture(t)
	offlineCalendar(e)
	now := holidayTime("2026-10-01", "04:59")
	h.advance(now)
	c.SkipCNHolidays = true
	c.Schedules = []Schedule{{"morning", "05:00", true}}
	setDateConfig(e, c, now)
	if _, err := e.ImportHolidays([]byte(holidayCNFixture), true, now); err != nil {
		t.Fatal(err)
	}
	if e.holidays.Calendar(c, now).Years[0].Known {
		t.Fatal("preview changed the calendar")
	}
	if _, err := e.ImportHolidays([]byte(holidayCNFixture), false, now); err != nil {
		t.Fatal(err)
	}
	status := e.Status(now)
	if status.Schedules[0].Time.Format("2006-01-02") != "2026-10-02" || !status.HolidayStatus.Days[0].Skip {
		t.Fatal("preview did not skip holiday")
	}
	h.advance(now.Add(time.Minute))
	e.Tick(h.now())
	idle(t, e)
	if h.calls["a"] != 0 || slot(t, e, "a").LastError != "holiday" {
		t.Fatal("execution disagreed with preview")
	}
}
