package keeper

import (
	"fmt"
	"testing"
	"time"
)

func TestWeeklyOnlyBusinessQuota(t *testing.T) {
	now := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	reset := now.Add(7 * 24 * time.Hour)
	body := fmt.Sprintf(`{"rate_limit":{"primary_window":{"limit_window_seconds":604800,"used_percent":32,"reset_at":%d},"secondary_window":null}}`, reset.Unix())
	q, err := ParseQuota([]byte(body), now)
	if err != nil || q.Known || q.Weekly == nil || q.Weekly.UsedPercent != 32 || !q.Weekly.ResetAt.Equal(reset) || q.Error != "" {
		t.Fatalf("weekly-only plan was treated as unknown: %+v %v", q, err)
	}
	if !q.ObservedAt.Equal(now) {
		t.Fatal("observation timestamp lost")
	}
}

func TestQuotaWindowsAreMatchedByDurationInEitherOrder(t *testing.T) {
	now := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	fiveHour := `{"limit_window_seconds":18000,"used_percent":6,"reset_after_seconds":18000}`
	weekly := `{"limit_window_seconds":604800,"used_percent":2,"reset_after_seconds":604800}`
	for _, windows := range [][2]string{{fiveHour, weekly}, {weekly, fiveHour}} {
		body := fmt.Sprintf(`{"rate_limit":{"primary_window":%s,"secondary_window":%s}}`, windows[0], windows[1])
		q, err := ParseQuota([]byte(body), now)
		if err != nil || !q.Known || q.UsedPercent != 6 || !q.ResetAt.Equal(now.Add(5*time.Hour)) || q.Weekly == nil || q.Weekly.UsedPercent != 2 || !q.Weekly.ResetAt.Equal(now.Add(7*24*time.Hour)) {
			t.Fatalf("windows mixed or dropped: %+v %v", q, err)
		}
	}
}

func TestInvalidWeeklyWindowDoesNotDiscardFiveHourQuota(t *testing.T) {
	now := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	for _, invalid := range []string{
		`{"limit_window_seconds":604800,"used_percent":101,"reset_after_seconds":604800}`,
		`{"limit_window_seconds":604800,"used_percent":-1,"reset_after_seconds":604800}`,
		`{"limit_window_seconds":604800,"used_percent":32}`,
	} {
		body := fmt.Sprintf(`{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":6,"reset_after_seconds":18000},"secondary_window":%s}}`, invalid)
		q, err := ParseQuota([]byte(body), now)
		if err != nil || !q.Known || q.Weekly != nil || q.UsedPercent != 6 {
			t.Fatalf("invalid weekly window corrupted valid quota: %+v %v", q, err)
		}
	}
}
