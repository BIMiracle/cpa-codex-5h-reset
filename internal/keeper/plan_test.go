package keeper

import (
	"errors"
	"testing"
)

func TestWakeOnlyIncludesPlus(t *testing.T) {
	for _, source := range []string{"manual", "scheduled"} {
		t.Run(source, func(t *testing.T) {
			e, h, _ := fixture(t)
			h.auths = []Auth{
				{ID: "plus", Provider: "codex", PlanType: " Plus "},
				{ID: "business", Provider: "codex", PlanType: "business_premium"},
				{ID: "business-label", Provider: "codex", PlanType: "Business Premium"},
				{ID: "team", Provider: "codex", PlanType: "team"},
				{ID: "pro", Provider: "codex", PlanType: "pro"},
				{ID: "free", Provider: "codex", PlanType: "free"},
				{ID: "enterprise", Provider: "codex", PlanType: "enterprise"},
				{ID: "unknown", Provider: "codex"},
				{ID: "not-exact-plus", Provider: "codex", PlanType: "plus_premium"},
				{ID: "disabled", Provider: "codex", PlanType: "plus", Disabled: true},
				{ID: "other", Provider: "claude", PlanType: "plus"},
			}
			h.codes["plus"] = []int{200}
			if source == "manual" {
				keys, err := e.Manual("", h.now())
				if err != nil || len(keys) != 1 {
					t.Fatalf("manual selection: %v %v", keys, err)
				}
			} else {
				e.cfg.Schedules = []Schedule{{"test", "15:02", true}}
				e.Tick(h.now())
			}
			idle(t, e)
			if len(h.calls) != 1 || h.calls["plus"] != 1 || len(e.Status(h.now()).Slots) != 1 {
				t.Fatalf("non-Plus account woke: calls=%v slots=%+v", h.calls, e.Status(h.now()).Slots)
			}
			// An explicit account selection cannot bypass the plan restriction.
			for _, a := range h.auths[1:] {
				e.cfg.AuthIDs = []string{a.ID}
				for _, id := range []string{a.ID, ""} {
					if _, err := e.Manual(id, h.now()); !errors.Is(err, ErrNoAuth) {
						t.Fatalf("%s with selection %s: %v", a.ID, id, err)
					}
				}
			}
			if len(h.calls) != 1 || len(e.Status(h.now()).Slots) != 1 {
				t.Fatal("rejected requests created work")
			}
		})
	}
}

func TestPlanChangeCancelsRetriesIncludingAfterRestart(t *testing.T) {
	for _, plan := range []string{"business_premium", ""} {
		for _, restart := range []bool{false, true} {
			for _, source := range []string{"manual", "scheduled"} {
				t.Run(plan+"/"+source+map[bool]string{true: "/restart", false: "/running"}[restart], func(t *testing.T) {
					e, h, c := fixture(t)
					if _, err := e.Manual("a", h.now()); err != nil {
						t.Fatal(err)
					}
					idle(t, e)
					before := slot(t, e, "a")
					e.mu.Lock()
					e.slots[before.Key].Source = source
					err := e.saveLocked()
					e.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
					h.auths[0].PlanType = plan
					if restart {
						e.Stop()
						e = New(h)
						e.now = h.now
						if err := e.Configure(c); err != nil {
							t.Fatal(err)
						}
						e.active = true
						defer e.Stop()
					}
					h.advance(before.NextAttempt)
					e.Tick(h.now())
					idle(t, e)
					after := slot(t, e, "a")
					if after.Status != "cancelled" || after.LastError != "plan_not_plus" || !after.Complete() || !after.NextAttempt.IsZero() || h.calls["a"] != 1 || h.notified != 0 {
						t.Fatalf("retry was not cancelled: %+v, calls=%v notifications=%d", after, h.calls, h.notified)
					}
				})
			}
		}
	}
}
