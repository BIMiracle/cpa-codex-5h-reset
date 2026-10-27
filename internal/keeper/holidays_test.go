package keeper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const holidayCNFixture = `{"year":2026,"days":[{"date":"2026-10-01","name":"国庆节","isOffDay":true},{"date":"2026-10-10","name":"国庆节补班","isOffDay":false}]}`
const timorFixture = `{"code":0,"holiday":{"10-01":{"date":"2026-10-01","name":"国庆节","holiday":true},"10-10":{"date":"2026-10-10","name":"国庆节补班","holiday":false}}}`

func holidayTime(date, clock string) time.Time {
	t, err := time.Parse("2006-01-02 15:04 -0700", date+" "+clock+" +0800")
	if err != nil {
		panic(err)
	}
	return t
}

func waitHolidays(t *testing.T, h *Holidays) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		busy := h.refreshing
		h.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("holiday refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHolidaySourceFormatsAndValidation(t *testing.T) {
	for _, raw := range []string{holidayCNFixture, timorFixture} {
		data, err := ParseHolidayData([]byte(raw), 0)
		if err != nil || data.Year != 2026 || len(data.Days) != 2 || !data.Days[0].IsOffDay || data.Days[1].IsOffDay {
			t.Fatalf("%+v %v", data, err)
		}
		if _, err := ParseHolidayData([]byte(raw), 2027); err == nil {
			t.Fatal("wrong year accepted")
		}
	}
	for _, raw := range []string{
		`not json`, `{"year":2026,"days":[]}`, `{"code":-1,"holiday":{}}`,
		`{"year":2026,"days":[{"date":"2026-02-29","name":"坏日期","isOffDay":true}]}`,
		`{"year":2026,"days":[{"date":"2026-10-01","name":"缺布尔值"}]}`,
		`{"year":2026,"days":[{"date":"2026-10-01","name":"","isOffDay":true}]}`,
		`{"year":2026,"days":[{"date":"2026-10-01","name":"a","isOffDay":true},{"date":"2026-10-01","name":"b","isOffDay":false}]}`,
		`{"code":0,"holiday":{"10-02":{"date":"2026-10-01","name":"国庆","holiday":true}}}`,
		`{"year":2026,"days":[{"date":"2025-01-01","name":"错年","isOffDay":true}]}`,
		`{"code":0,"holiday":{"10-01":{"date":"2026-10-01","name":"国庆","holiday":true},"01-01":{"date":"2027-01-01","name":"元旦","holiday":true}}}`,
	} {
		if _, err := ParseHolidayData([]byte(raw), 0); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := ParseHolidayData(make([]byte, maxHolidayBytes+1), 0); err == nil {
		t.Fatal("oversized data accepted")
	}
}

func TestHolidayHTTPTransportIsPublicAndBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("ChatGPT-Account-Id") != "" {
			t.Error("credentials reached holiday server")
		}
		if r.URL.Path == "/fail" {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(timorFixture))
	}))
	defer server.Close()
	if raw, err := fetchHolidayHTTP(context.Background(), server.URL); err != nil || string(raw) != timorFixture {
		t.Fatalf("%s %v", raw, err)
	}
	if _, err := fetchHolidayHTTP(context.Background(), server.URL+"/fail"); err == nil {
		t.Fatal("non-200 accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetchHolidayHTTP(ctx, server.URL); err == nil {
		t.Fatal("cancelled context accepted")
	}
}

func TestHolidayFallbackCachingAndFailurePolicy(t *testing.T) {
	var mu sync.Mutex
	calls := []string{}
	fail := false
	h := NewHolidays(func(_ context.Context, url string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, url)
		if fail || strings.Contains(url, "timor.tech") {
			return nil, errors.New("unavailable")
		}
		if strings.Contains(url, "raw.githubusercontent.com") {
			return []byte(`{"year":2026,"days":[]}`), nil
		}
		return []byte(holidayCNFixture), nil
	})
	c := Defaults()
	c.SkipCNHolidays = true
	now := holidayTime("2026-10-01", "00:00")
	path := filepath.Join(t.TempDir(), "cache.json")
	h.Configure(path)
	if !h.Refresh(c, now, false) {
		t.Fatal("not started")
	}
	waitHolidays(t, h)
	if len(calls) != 3 || !strings.Contains(calls[2], "cdn.jsdelivr.net") {
		t.Fatal(calls)
	}
	if h.Refresh(c, now.Add(time.Hour), false) {
		t.Fatal("refreshed fresh data")
	}
	if DateBlockReason(c, now, h) != "holiday" || DateBlockReason(c, holidayTime("2026-10-10", "05:00"), h) != "" {
		t.Fatal("holiday/workday policy")
	}
	restored := NewHolidays(nil)
	restored.Configure(path)
	if DateBlockReason(c, now, restored) != "holiday" {
		t.Fatal("cache was not restored")
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	h.Refresh(c, now.Add(25*time.Hour), false)
	waitHolidays(t, h)
	calendar := h.Calendar(c, now)
	if !strings.Contains(calendar.Error, "正在使用缓存") || !calendar.Years[0].Known {
		t.Fatalf("%+v", calendar)
	}
	if h.Refresh(c, now.Add(25*time.Hour+time.Minute), false) {
		t.Fatal("failed source was retried too soon")
	}
	if !h.Refresh(c, now.Add(25*time.Hour+30*time.Minute), false) {
		t.Fatal("failed source was not retried")
	}
	waitHolidays(t, h)
	missing := NewHolidays(func(context.Context, string) ([]byte, error) { return nil, errors.New("down") })
	missing.Configure(filepath.Join(t.TempDir(), "missing.json"))
	missing.Refresh(c, now, false)
	waitHolidays(t, missing)
	if DateBlockReason(c, now, missing) != "" || DateBlockReason(c, holidayTime("2026-10-03", "00:00"), missing) != "weekend" {
		t.Fatal("no-data fallback must only skip weekends")
	}
	if !strings.Contains(missing.Calendar(c, now).Error, "可手动导入") {
		t.Fatal("missing-data warning")
	}
}

func TestImportPriorityPersistenceAndWriteFailure(t *testing.T) {
	h := NewHolidays(nil)
	path := filepath.Join(t.TempDir(), "cache.json")
	h.Configure(path)
	data, _ := ParseHolidayData([]byte(holidayCNFixture), 0)
	now := holidayTime("2026-10-01", "00:00")
	if err := h.Import(data, now); err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	c.SkipCNHolidays = true
	if h.Refresh(c, now, true) {
		t.Fatal("manual annual data should not be refreshed")
	}
	h.mu.Lock()
	online := data
	online.Days = []HolidayDay{{"2026-10-01", "在线补班", false}, {"2026-10-02", "旧在线放假", true}}
	h.years[2026] = online
	h.mu.Unlock()
	if DateBlockReason(c, now, h) != "holiday" {
		t.Fatal("online data overrode imported holiday")
	}
	if DateBlockReason(c, holidayTime("2026-10-02", "00:00"), h) != "" {
		t.Fatal("an imported annual file must fully replace its online year")
	}
	restored := NewHolidays(nil)
	restored.Configure(path)
	if !restored.Calendar(c, now).Years[0].Manual {
		t.Fatal("manual data lost after restart")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.path = filepath.Join(blocked, "cache.json")
	h.mu.Unlock()
	if err := h.Import(online, now); err == nil || DateBlockReason(c, now, h) != "holiday" {
		t.Fatal("failed import must preserve old data")
	}
	if err := h.ClearImport(2026); err == nil || !h.Calendar(c, now).Years[0].Manual {
		t.Fatal("failed clearing must preserve old import")
	}
	h.mu.Lock()
	h.path = path
	h.mu.Unlock()
	if err := h.ClearImport(2026); err != nil || DateBlockReason(c, now, h) != "" {
		t.Fatal("clear did not restore online data")
	}
	restored.Configure(path + ".other")
	restored.Configure(path)
	if restored.Calendar(c, now).Years[0].Manual {
		t.Fatal("cleared import returned after restart")
	}
}

func TestCalendarCrossYearAndNewerAnnouncement(t *testing.T) {
	h := NewHolidays(nil)
	h.Configure(filepath.Join(t.TempDir(), "cache.json"))
	data, err := ParseHolidayData([]byte(`{"year":2027,"days":[{"date":"2026-12-31","name":"元旦连休","isOffDay":true},{"date":"2027-01-01","name":"元旦","isOffDay":true}]}`), 2027)
	if err != nil {
		t.Fatal(err)
	}
	now := holidayTime("2026-12-20", "12:00")
	if err := h.Import(data, now); err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	c.SkipCNHolidays = true
	calendar := h.Calendar(c, now)
	if len(calendar.Days) != 30 || len(calendar.Years) != 2 || calendar.Days[11].Kind != "holiday" || calendar.Days[12].Kind != "holiday" {
		t.Fatalf("%+v", calendar)
	}
}

func TestMonthCalendarDisplayRangeAndCrossYearLabels(t *testing.T) {
	for _, tc := range []struct{ month, start string }{
		{"2026-10-02", "2026-09-28"},
		{"2027-02-01", "2027-02-01"},
		{"2028-02-02", "2028-01-31"},
		{"2027-01-02", "2026-12-28"},
	} {
		at := holidayTime(tc.month, "12:00")
		if start := monthGridStart(at); start.Weekday() != time.Monday || start.Format("2006-01-02") != tc.start {
			t.Fatalf("%s: %v", tc.month, start)
		}
	}
	h := NewHolidays(nil)
	h.Configure(filepath.Join(t.TempDir(), "calendar.json"))
	now := holidayTime("2026-01-01", "00:30")
	for _, data := range []HolidayYear{
		{Year: 2025, Days: []HolidayDay{{"2025-12-31", "跨年放假", true}}},
		{Year: 2026, Days: []HolidayDay{{"2026-01-01", "元旦", true}}},
	} {
		if err := h.Import(data, now); err != nil {
			t.Fatal(err)
		}
	}
	c := Defaults()
	c.SkipCNHolidays = true
	calendar := h.Calendar(c, now)
	if len(calendar.Days) != 30 || calendar.Days[0].Date != "2026-01-01" {
		t.Fatal("next-30-day API changed")
	}
	if len(calendar.Years) != 2 || calendar.Years[0].Year != 2025 || calendar.Years[1].Year != 2026 {
		t.Fatal("previous calendar year not included", calendar.Years)
	}
	start, end := calendarDisplayRange(now)
	if calendar.DisplayDays[0].Date != start.Format("2006-01-02") || calendar.DisplayDays[len(calendar.DisplayDays)-1].Date != end.AddDate(0, 0, -1).Format("2006-01-02") {
		t.Fatal("incomplete month grids")
	}
	for i, day := range calendar.DisplayDays {
		if day.Date != start.AddDate(0, 0, i).Format("2006-01-02") {
			t.Fatal("nonconsecutive display dates", day)
		}
		if day.Date == "2025-12-31" || day.Date == "2026-01-01" {
			if day.Kind != "holiday" || !day.DataKnown || !day.Skip || day.Reason != "holiday" {
				t.Fatal("cross-year holiday label lost", day)
			}
		}
	}
	c.Timezone = "America/Los_Angeles"
	calendar = h.Calendar(c, now)
	if calendar.Days[0].Date != "2025-12-31" || !calendar.Days[0].Skip {
		t.Fatal("calendar did not use selected timezone")
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	local := now.In(loc)
	start, _ = calendarDisplayRange(local)
	if calendar.DisplayDays[0].Date != start.Format("2006-01-02") {
		t.Fatal("month range used server timezone")
	}
}

func TestDateRulesBoundariesTimezoneAndNextRuns(t *testing.T) {
	c := Defaults()
	c.SkipCNHolidays = true
	c.OneTimeRule = OneTimeRule{"exclude", "2026-10-02", "2026-10-04"}
	for _, tc := range []struct{ date, want string }{{"2026-10-01", ""}, {"2026-10-02", "excluded_date"}, {"2026-10-04", "excluded_date"}, {"2026-10-05", ""}} {
		if got := DateBlockReason(c, holidayTime(tc.date, "12:00"), nil); got != tc.want {
			t.Fatalf("%s: %s", tc.date, got)
		}
	}
	runs := NextRuns(c, holidayTime("2026-10-01", "23:59"))
	for _, run := range runs {
		if run.Time.Format("2006-01-02") != "2026-10-05" {
			t.Fatal(run)
		}
	}
	c.OneTimeRule = OneTimeRule{"wake_tomorrow", "2026-10-03", "2026-10-03"}
	if DateBlockReason(c, holidayTime("2026-10-03", "05:00"), nil) != "" || DateBlockReason(c, holidayTime("2026-10-04", "05:00"), nil) != "weekend" {
		t.Fatal("one-time wake did not expire")
	}
	if c.OneTimeRule.Effective(holidayTime("2026-10-04", "00:00"), c.Timezone).Mode != "none" {
		t.Fatal("expired rule still active")
	}
	c.Timezone = "America/Los_Angeles"
	if DateBlockReason(c, holidayTime("2026-10-04", "00:00"), nil) != "" {
		t.Fatal("rule used server date instead of selected timezone")
	}
	for _, rule := range []OneTimeRule{{"bad", "", ""}, {"exclude", "2026-10-02", "2026-10-01"}, {"exclude", "2026-10-01", "2026-10-31"}, {"wake_tomorrow", "2026-10-01", "2026-10-02"}, {"exclude", "2026-02-29", "2026-03-01"}} {
		c.OneTimeRule = rule
		if c.Validate() == nil {
			t.Fatal("invalid rule accepted", rule)
		}
	}
	c.OneTimeRule = OneTimeRule{"exclude", "2025-01-01", "2025-01-03"}
	if c.Validate() != nil {
		t.Fatal("expired persisted rule must still load")
	}
}

func TestOldHolidayResponsesCannotOverwriteNewConfiguration(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	h := NewHolidays(func(context.Context, string) ([]byte, error) {
		close(started)
		<-release
		return []byte(holidayCNFixture), nil
	})
	h.Configure(filepath.Join(t.TempDir(), "old.json"))
	h.Refresh(Defaults(), holidayTime("2026-10-01", "00:00"), true)
	<-started
	newPath := filepath.Join(t.TempDir(), "new.json")
	h.Configure(newPath)
	close(release)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.years) != 0 || h.path != newPath {
		t.Fatal("old snapshot leaked into new state path")
	}
}
