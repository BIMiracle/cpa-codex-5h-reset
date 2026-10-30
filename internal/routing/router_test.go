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
	used       map[string]float64
	weeklyUsed map[string]float64
	weeklyOnly map[string]bool
	fail       bool
	failIDs    map[string]bool
	auths      []keeper.Auth
	calls      int
	now        time.Time
}

func (h *fakeHost) ListAuths() ([]keeper.Auth, error) {
	if h.auths != nil {
		return h.auths, nil
	}
	return []keeper.Auth{{ID: "a.json", AuthIndex: "a", Provider: "codex"}, {ID: "b.json", AuthIndex: "b", Provider: "codex"}}, nil
}
func (h *fakeHost) FetchQuota(a keeper.Auth, now time.Time) (keeper.Quota, error) {
	h.calls++
	if h.fail || h.failIDs[a.ID] {
		return keeper.Quota{}, errors.New("never expose secret-token")
	}
	q := keeper.Quota{Known: !h.weeklyOnly[a.ID], UsedPercent: h.used[a.ID], ResetAt: h.now.Add(5 * time.Hour), ObservedAt: now}
	if used, ok := h.weeklyUsed[a.ID]; ok {
		q.Weekly = &keeper.QuotaWindow{UsedPercent: used, ResetAt: h.now.Add(7 * 24 * time.Hour)}
	}
	return q, nil
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
	return &fakeHost{now: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), used: map[string]float64{"a.json": 21, "b.json": 21}}
}

func picked(t *testing.T, r *Router, req Request, want string) {
	t.Helper()
	if got := r.Pick(req); !got.Handled || got.Reject || got.AuthID != want {
		t.Fatalf("pick=%+v, want %s", got, want)
	}
}

func TestNewSessionBalanceAndExistingSessionAffinity(t *testing.T) {
	h := fixture()
	r := configured(t, h, "")
	picked(t, r, request("old-session"), "a.json")
	h.used = map[string]float64{"a.json": 22, "b.json": 20}
	h.now = h.now.Add(2 * time.Minute)
	picked(t, r, request("old-session"), "a.json")
	if h.calls != 4 {
		t.Fatal("active session did not refresh expired quota", h.calls)
	}
	picked(t, r, request("new-session"), "b.json")
	picked(t, r, request("old-session"), "a.json")
	if h.calls != 4 {
		t.Fatal("quota cache not reused", h.calls)
	}
	// Host retries remove the failed/limited auth from Candidates.
	retry := request("old-session")
	retry.Candidates = retry.Candidates[1:]
	picked(t, r, retry, "b.json")
	picked(t, r, request("old-session"), "b.json")
}

func mixedPlanPool() (*fakeHost, []Candidate) {
	h := fixture()
	h.auths = []keeper.Auth{
		{ID: "a-business.json", AuthIndex: "a", Provider: "codex", PlanType: "self_serve_business_prolite"},
		{ID: "b-plus.json", AuthIndex: "b", Provider: "codex", PlanType: "plus"},
		{ID: "c-plus.json", AuthIndex: "c", Provider: "codex", PlanType: "plus"},
	}
	h.used = map[string]float64{"a-business.json": 1, "b-plus.json": 3, "c-plus.json": 5}
	candidates := []Candidate{}
	for _, a := range h.auths {
		candidates = append(candidates, Candidate{ID: a.ID, Provider: a.Provider})
	}
	return h, candidates
}

func TestBusinessAndPlusAccountsRotateWhenQuotasAreClose(t *testing.T) {
	h, candidates := mixedPlanPool()
	r := configured(t, h, "")
	counts := map[string]int{}
	for i := range 18 {
		req := request(fmt.Sprintf("balanced-%d", i))
		req.Candidates = candidates
		got := r.Pick(req)
		if !got.Handled || got.Reject || got.AuthID == "" {
			t.Fatalf("new session rejected: %+v", got)
		}
		counts[got.AuthID]++
		// Repeated turns keep affinity without consuming another rotation slot.
		picked(t, r, req, got.AuthID)
	}
	for _, c := range candidates {
		if counts[c.ID] != 6 {
			t.Fatalf("nearby quota observations did not rotate equally: %v", counts)
		}
	}
	if h.calls != 3 {
		t.Fatalf("quota cache was bypassed: %d", h.calls)
	}
}

func TestWeeklyHeadroomFavorsPlusAndRebalancesBusinessSession(t *testing.T) {
	h, candidates := mixedPlanPool()
	// Match the reported remaining percentages: weekly 68/98/96 and
	// five-hour 94/76 for Plus; Business exposes no five-hour window.
	h.weeklyOnly = map[string]bool{"a-business.json": true}
	h.weeklyUsed = map[string]float64{"a-business.json": 32, "b-plus.json": 2, "c-plus.json": 4}
	h.used = map[string]float64{"b-plus.json": 6, "c-plus.json": 24}
	r := configured(t, h, "")
	old := request("old-business-session")
	old.Candidates = candidates[:1]
	picked(t, r, old, "a-business.json")
	old.Candidates = candidates
	picked(t, r, old, "b-plus.json")
	for i := range 3 {
		req := request(fmt.Sprintf("headroom-%d", i))
		req.Candidates = candidates
		picked(t, r, req, "b-plus.json")
	}
	picked(t, r, old, "b-plus.json")
	if h.calls != 3 {
		t.Fatalf("quota cache was not shared across old and new sessions: %d", h.calls)
	}
}

func TestWeeklyUsageWinsOverFiveHourUsage(t *testing.T) {
	h := fixture()
	h.used = map[string]float64{"a.json": 1, "b.json": 80}
	h.weeklyUsed = map[string]float64{"a.json": 32, "b.json": 2}
	r := configured(t, h, "")
	picked(t, r, request("week-first"), "b.json")
}

func TestActiveSessionRefreshesAndRebalancesWithoutNewSessions(t *testing.T) {
	h := fixture()
	h.weeklyUsed = map[string]float64{"a.json": 0, "b.json": 2}
	r := configured(t, h, "")
	picked(t, r, request("long-running"), "a.json")
	h.weeklyUsed["a.json"] = 9
	h.now = h.now.Add(time.Minute)
	picked(t, r, request("long-running"), "a.json")
	h.weeklyUsed["a.json"] = 32
	h.now = h.now.Add(time.Minute)
	picked(t, r, request("long-running"), "b.json")
	if h.calls != 6 {
		t.Fatalf("long session did not keep quota fresh: %d", h.calls)
	}
}

func TestFiveHourHeadroomAndRebindTolerance(t *testing.T) {
	h := fixture()
	h.weeklyUsed = map[string]float64{"a.json": 2, "b.json": 4}
	h.used = map[string]float64{"a.json": 10, "b.json": 12}
	r := configured(t, h, "")
	picked(t, r, request("active"), "a.json")
	h.used["a.json"], h.used["b.json"] = 30, 12
	h.now = h.now.Add(time.Minute)
	picked(t, r, request("active"), "a.json")
	picked(t, r, request("new"), "b.json")
	h.used["a.json"] = 40
	h.now = h.now.Add(time.Minute)
	picked(t, r, request("active"), "b.json")
}

func TestWeeklyOnlyExhaustionAndReset(t *testing.T) {
	h := fixture()
	h.weeklyOnly = map[string]bool{"a.json": true}
	h.weeklyUsed = map[string]float64{"a.json": 100, "b.json": 20}
	r := configured(t, h, "")
	picked(t, r, request("before-reset"), "b.json")
	q := r.quotas["a.json"]
	q.Weekly.ResetAt = h.now.Add(time.Second)
	r.quotas["a.json"] = q
	h.weeklyUsed["a.json"] = 0
	h.now = h.now.Add(2 * time.Second)
	picked(t, r, request("after-reset"), "a.json")
	if h.calls != 3 {
		t.Fatalf("weekly reset did not invalidate just the expired window: %d", h.calls)
	}
	h.weeklyUsed["a.json"], h.weeklyUsed["b.json"] = 100, 100
	h.now = h.now.Add(time.Minute)
	if got := r.Pick(request("both-exhausted")); !got.Reject || got.RejectCode != "quota_exhausted" {
		t.Fatalf("exhausted weekly pool accepted: %+v", got)
	}
}

func TestMissingFiveHourQuotaIsNotTreatedAsZeroUsage(t *testing.T) {
	h, candidates := mixedPlanPool()
	h.weeklyOnly = map[string]bool{"a-business.json": true}
	h.weeklyUsed = map[string]float64{"a-business.json": 4, "b-plus.json": 4, "c-plus.json": 4}
	h.used = map[string]float64{"b-plus.json": 30, "c-plus.json": 32}
	r := configured(t, h, "")
	for i := range 6 {
		req := request(fmt.Sprintf("missing-window-%d", i))
		req.Candidates = candidates
		picked(t, r, req, candidates[i%len(candidates)].ID)
	}
}

func TestUnknownPlusQuotaStillSharesRotation(t *testing.T) {
	h, candidates := mixedPlanPool()
	h.failIDs = map[string]bool{"b-plus.json": true, "c-plus.json": true}
	r := configured(t, h, "")
	for i := range 6 {
		req := request(fmt.Sprintf("unknown-%d", i))
		req.Candidates = candidates
		picked(t, r, req, candidates[i%len(candidates)].ID)
	}
	if h.calls != 3 {
		t.Fatalf("unknown quota was repeatedly queried: %d", h.calls)
	}
}

func TestExhaustedAccountsAreSkippedAndRejoinAfterReset(t *testing.T) {
	h, candidates := mixedPlanPool()
	h.used["b-plus.json"] = 100
	r := configured(t, h, "")
	for i, want := range []string{"a-business.json", "c-plus.json", "a-business.json", "c-plus.json"} {
		req := request(fmt.Sprintf("exhausted-%d", i))
		req.Candidates = candidates
		picked(t, r, req, want)
	}
	q := r.quotas["b-plus.json"]
	q.ResetAt = h.now.Add(time.Second)
	r.quotas["b-plus.json"] = q
	h.now = h.now.Add(2 * time.Second)
	h.used["b-plus.json"] = 0
	for i, want := range []string{"a-business.json", "b-plus.json", "c-plus.json"} {
		req := request(fmt.Sprintf("reset-%d", i))
		req.Candidates = candidates
		picked(t, r, req, want)
	}
	if h.calls != 4 {
		t.Fatalf("reset did not refresh exactly the expired account: %d", h.calls)
	}
}

func TestExhaustedPriorityTierFallsBackAndBoundSessionMoves(t *testing.T) {
	h := fixture()
	r := configured(t, h, "")
	req := request("sticky")
	req.Candidates[0].Priority = 10
	picked(t, r, req, "a.json")
	h.used["a.json"] = 100
	h.now = h.now.Add(time.Minute)
	other := req
	other.Options = request("new").Options
	picked(t, r, other, "b.json")
	// Fresh exhaustion also invalidates the old binding while the host still
	// includes that auth in its candidate list.
	picked(t, r, req, "b.json")
	h.used["a.json"] = 0
	h.now = h.now.Add(time.Minute)
	other.Options = request("recovered").Options
	picked(t, r, other, "a.json")
	picked(t, r, req, "b.json")
}

func TestBoundSessionFailsOverToExplicitBackupAndKeepsItAfterRecovery(t *testing.T) {
	h := fixture()
	h.used["b.json"] = 0
	r := configured(t, h, "  bindings:\n    - key: client-key\n      allow: [a.*]\n      fallback_allow: [b.*]\n")
	req := request("ongoing")
	req.Candidates[1].Priority = 9
	picked(t, r, req, "a.json")
	h.used["a.json"] = 100
	h.now = h.now.Add(time.Minute)
	picked(t, r, req, "b.json")
	// A restart must recover the replacement binding, not the exhausted one.
	restarted := New(h)
	restarted.now = r.now
	plan, err := restarted.Prepare(r.config)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Configure(plan)
	picked(t, restarted, req, "b.json")
	h.used["a.json"] = 0
	h.now = h.now.Add(time.Minute)
	picked(t, restarted, req, "b.json")
	newSession := request("new-after-recovery")
	newSession.Candidates = req.Candidates
	picked(t, restarted, newSession, "a.json")
}

func TestBackupsCoverUnavailablePrimariesButDoNotEscapeTheirAllowList(t *testing.T) {
	h, candidates := mixedPlanPool()
	r := configured(t, h, "  bindings:\n    - key: client-key\n      allow: [b-plus.json]\n      fallback_allow: [c-plus.json]\n")
	req := request("ongoing")
	req.Candidates = candidates
	picked(t, r, req, "b-plus.json")
	// The unrelated Business account has more quota and priority, but is not
	// authorized for this key. The host removed the unavailable primary.
	req.Candidates = []Candidate{candidates[0], candidates[2]}
	req.Candidates[0].Priority = 99
	picked(t, r, req, "c-plus.json")
	req.Candidates = req.Candidates[:1]
	if got := r.Pick(req); !got.Reject || got.RejectCode != "bound_auth_unavailable" {
		t.Fatalf("fallback escaped its explicit pool: %+v", got)
	}
}

func TestWeeklyExhaustionUsesBackupAndBothExhaustedReject(t *testing.T) {
	h := fixture()
	h.weeklyOnly = map[string]bool{"a.json": true}
	h.weeklyUsed = map[string]float64{"a.json": 99, "b.json": 2}
	r := configured(t, h, "  bindings:\n    - key: client-key\n      allow: [a.json]\n      fallback_allow: [b.json]\n")
	picked(t, r, request("old-weekly-session"), "a.json")
	h.weeklyUsed["a.json"] = 100
	h.now = h.now.Add(time.Minute)
	picked(t, r, request("old-weekly-session"), "b.json")
	h.weeklyUsed["b.json"] = 100
	h.now = h.now.Add(time.Minute)
	if got := r.Pick(request("old-weekly-session")); !got.Reject || got.RejectCode != "quota_exhausted" {
		t.Fatalf("fully exhausted backup pool accepted: %+v", got)
	}
}

func TestFailedRefreshPreservesExhaustionUntilReset(t *testing.T) {
	for _, weekly := range []bool{false, true} {
		t.Run(fmt.Sprint("weekly=", weekly), func(t *testing.T) {
			h := fixture()
			r := configured(t, h, "")
			picked(t, r, request("ongoing"), "a.json")
			reset := h.now.Add(5 * time.Hour)
			q := r.quotas["a.json"]
			if weekly {
				q.Known = false
				q.Weekly = &keeper.QuotaWindow{UsedPercent: 100, ResetAt: reset}
			} else {
				q.UsedPercent, q.ResetAt = 100, reset
			}
			r.quotas["a.json"] = q
			h.failIDs = map[string]bool{"a.json": true}
			h.now = h.now.Add(time.Minute)
			picked(t, r, request("ongoing"), "b.json")
			h.now = h.now.Add(time.Minute)
			picked(t, r, request("new-before-reset"), "b.json")
			if r.quotaAvailable("a.json", h.now) || r.quotas["a.json"].Error == "" {
				t.Fatal("failed quota lookup resurrected confirmed exhaustion")
			}
			h.now = reset.Add(time.Second)
			picked(t, r, request("new-after-reset"), "a.json")
		})
	}
}

func TestExhaustedBackupBindingIsNotAcceptedWhenStateWriteFails(t *testing.T) {
	h := fixture()
	r := configured(t, h, "  bindings:\n    - key: client-key\n      allow: [a.json]\n      fallback_allow: [b.json]\n")
	picked(t, r, request("ongoing"), "a.json")
	scope := digest("client-key\x00codex\x00gpt-6.1-sol")
	key := digest(scope + "\x00ongoing")
	h.used["a.json"] = 100
	h.now = h.now.Add(time.Minute)
	r.config.StateFile = t.TempDir()
	if got := r.Pick(request("ongoing")); !got.Reject || got.RejectCode != "routing_state_write_failed" || r.bindings[key].AuthID != "a.json" {
		t.Fatalf("unpersisted fallback binding accepted: %+v", got)
	}
}

func TestPartialRefreshDoesNotDiscardExhaustedWindow(t *testing.T) {
	for _, weekly := range []bool{false, true} {
		t.Run(fmt.Sprint("weekly=", weekly), func(t *testing.T) {
			h := fixture()
			r := configured(t, h, "")
			picked(t, r, request("ongoing"), "a.json")
			q := r.quotas["a.json"]
			if weekly {
				q.Weekly = &keeper.QuotaWindow{UsedPercent: 100, ResetAt: h.now.Add(7 * 24 * time.Hour)}
			} else {
				q.UsedPercent = 100
				h.weeklyOnly = map[string]bool{"a.json": true}
				h.weeklyUsed = map[string]float64{"a.json": 20}
			}
			r.quotas["a.json"] = q
			h.now = h.now.Add(time.Minute)
			picked(t, r, request("ongoing"), "b.json")
			if r.quotaAvailable("a.json", h.now) {
				t.Fatal("partial observation cleared a missing exhausted window")
			}
		})
	}
}

func TestRoundRobinCursorSurvivesRestart(t *testing.T) {
	h, candidates := mixedPlanPool()
	r := configured(t, h, "")
	for i := range 6 {
		req := request(fmt.Sprintf("restart-%d", i))
		req.Candidates = candidates
		picked(t, r, req, candidates[i%len(candidates)].ID)
		restarted := New(h)
		restarted.now = r.now
		plan, err := restarted.Prepare(r.config)
		if err != nil {
			t.Fatal(err)
		}
		restarted.Configure(plan)
		r = restarted
	}
}

func TestLegacyBindingsLoadWithoutRotationCursor(t *testing.T) {
	h := fixture()
	r := configured(t, h, "")
	scope := digest("client-key\x00codex\x00gpt-6.1-sol")
	key := digest(scope + "\x00legacy-session")
	raw, err := json.Marshal(diskState{Schema: 1, Bindings: map[string]sessionBinding{
		key: {AuthID: "b.json", ExpiresAt: h.now.Add(time.Hour)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.config.StateFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	restarted := New(h)
	restarted.now = r.now
	plan, err := restarted.Prepare(r.config)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Configure(plan)
	picked(t, restarted, request("legacy-session"), "b.json")
	picked(t, restarted, request("new-session"), "a.json")
	picked(t, restarted, request("another-session"), "b.json")
}

func TestBindingsSurviveRestartReconfigureAndDisable(t *testing.T) {
	h := fixture()
	r := configured(t, h, "")
	picked(t, r, request("private-session"), "a.json")
	raw, err := os.ReadFile(r.config.StateFile)
	if err != nil || strings.Contains(string(raw), "client-key") || strings.Contains(string(raw), "private-session") {
		t.Fatalf("state leaks caller identity: %s %v", raw, err)
	}
	h.used = map[string]float64{"a.json": 23, "b.json": 21}
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
	picked(t, restarted, request("private-session"), "a.json")
	plan, _ = restarted.Prepare(r.config)
	restarted.Configure(plan)
	picked(t, restarted, request("private-session"), "a.json")
	h.now = h.now.Add(4*time.Hour + time.Second)
	picked(t, restarted, request("private-session"), "b.json")
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
	picked(t, r, otherKey, "a.json")
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
	h.used["a.json"], h.used["b.json"] = 22, 21
	other := request("same-id")
	other.Options.Headers["Authorization"] = []string{"Bearer Client-Key"}
	other.Options.Headers["session_id"] = []string{"primer"}
	picked(t, r, other, "a.json")
	other.Options.Headers["session_id"] = []string{"same-id"}
	picked(t, r, other, "b.json")
	other = request("same-id")
	other.Model = "gpt-6-luna"
	other.Options.Headers["session_id"] = []string{"primer"}
	picked(t, r, other, "a.json")
	other.Options.Headers["session_id"] = []string{"same-id"}
	picked(t, r, other, "b.json")
	picked(t, r, request("same-id"), "a.json")
	missing := request("")
	picked(t, r, missing, "b.json")
	picked(t, r, missing, "a.json")
	if len(r.bindings) != 5 {
		t.Fatal("request without stable identity created a binding")
	}
	missing.Options.Metadata = map[string]any{"execution_session_id": "execution-identity"}
	picked(t, r, missing, "b.json")
}

func TestExhaustedQuotaAndBrokenStateFailClosed(t *testing.T) {
	h := fixture()
	h.used = map[string]float64{"a.json": 100, "b.json": 100}
	r := configured(t, h, "")
	if got := r.Pick(request("empty")); !got.Reject || got.RejectCode != "quota_exhausted" {
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
	results := make(chan Response, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- r.Pick(request(fmt.Sprint(i)))
		}()
	}
	wg.Wait()
	close(results)
	counts := map[string]int{}
	for got := range results {
		if !got.Handled || got.Reject {
			t.Fatalf("concurrent pick: %+v", got)
		}
		counts[got.AuthID]++
	}
	if counts["a.json"] != 10 || counts["b.json"] != 10 {
		t.Fatalf("concurrent sessions were not balanced: %v", counts)
	}
	if h.calls != 2 || len(r.bindings) != 20 {
		t.Fatalf("parallel lookup duplication/binding loss: %d %d", h.calls, len(r.bindings))
	}
	for i := len(r.bindings); i < 4096; i++ {
		r.bindings[digest(fmt.Sprint(i)+"capacity")] = sessionBinding{AuthID: "a.json", ExpiresAt: h.now.Add(time.Hour)}
	}
	if got := r.Pick(request("capacity-new")); !got.Reject || got.RejectCode != "routing_session_capacity" {
		t.Fatal("capacity silently evicted active bindings", got)
	}
	existing := r.Pick(request("0"))
	if !existing.Handled || existing.Reject || existing.AuthID == "" {
		t.Fatal("existing binding rejected at capacity", existing)
	}
	retry := request("0")
	if existing.AuthID == "a.json" {
		retry.Candidates = retry.Candidates[1:]
	} else {
		retry.Candidates = retry.Candidates[:1]
	}
	picked(t, r, retry, retry.Candidates[0].ID)
}

func TestConfigurationValidation(t *testing.T) {
	for _, raw := range []string{
		"quota_routing: {session_ttl: 0s}",
		"quota_routing: {quota_cache_ttl: 24h}",
		"quota_routing: {unbound: allow}",
		"quota_routing: {bindings: [{key: secret-token, allow: ['[']}]}",
		"quota_routing: {bindings: [{key: secret-token, allow: []}]}",
		"quota_routing: {bindings: [{key: a, allow: ['*']}, {key: a, allow: ['*']}]}",
		"quota_routing: {bindings: [{key: secret-token, allow: ['*'], fallback_allow: ['[']}]}",
		"quota_routing: {bindings: [{key: a, allow: ['*'], fallback_allow: [' ']}]}",
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
	c, err = Parse([]byte("quota_routing: {bindings: [{key: a, allow: [' A.* '], fallback_allow: [' B.* ']}]}"), "state.json")
	if err != nil || c.Bindings[0].Allow[0] != "a.*" || c.Bindings[0].FallbackAllow[0] != "b.*" {
		t.Fatalf("fallback patterns were not normalized: %+v %v", c, err)
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
	picked(t, r, request("existing"), "a.json")
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
		if got.AuthID != "a.json" {
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

func TestConcurrentRefreshDoesNotReuseStaleExhaustedBinding(t *testing.T) {
	h := fixture()
	r := configured(t, h, "")
	picked(t, r, request("existing"), "a.json")
	h.used["a.json"] = 100
	q := r.quotas["a.json"]
	q.UsedPercent = 100
	r.quotas["a.json"] = q
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
		if got.AuthID != "b.json" || got.Reject {
			t.Errorf("stale exhausted binding was reused: %+v", got)
		}
	case <-time.After(time.Second):
		t.Error("exhaustion failover waited for unrelated quota refresh")
	}
	close(blocked.release)
	if got := <-done; got.AuthID != "b.json" || got.Reject {
		t.Fatal(got)
	}
}
