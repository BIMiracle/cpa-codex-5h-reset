package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxHolidayBytes = 2 << 20

type HolidayDay struct {
	Date     string `json:"date"`
	Name     string `json:"name"`
	IsOffDay bool   `json:"is_off_day"`
}

type HolidayYear struct {
	Year      int          `json:"year"`
	Source    string       `json:"source"`
	UpdatedAt time.Time    `json:"updated_at"`
	Days      []HolidayDay `json:"days"`
}

type HolidayYearStatus struct {
	Year      int       `json:"year"`
	Source    string    `json:"source,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Known     bool      `json:"known"`
	Manual    bool      `json:"manual"`
	OffDays   int       `json:"off_days"`
	Workdays  int       `json:"workdays"`
	Error     string    `json:"error,omitempty"`
}

type CalendarDay struct {
	Date      string `json:"date"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	DataKnown bool   `json:"data_known"`
	Skip      bool   `json:"skip"`
	Reason    string `json:"reason,omitempty"`
}

type HolidayCalendar struct {
	Timezone    string              `json:"timezone"`
	Now         time.Time           `json:"now"`
	Days        []CalendarDay       `json:"days"`
	DisplayDays []CalendarDay       `json:"display_days"`
	Years       []HolidayYearStatus `json:"years"`
	Refreshing  bool                `json:"refreshing"`
	Error       string              `json:"error,omitempty"`
}

type holidayDisk struct {
	Schema  int                 `json:"schema"`
	Years   map[int]HolidayYear `json:"years"`
	Imports map[int]HolidayYear `json:"imports"`
}

type HolidayFetch func(context.Context, string) ([]byte, error)

// Holidays owns immutable annual snapshots. Network calls never hold its mutex
// or the engine mutex. Generation checks discard responses from old configs.
type Holidays struct {
	mu         sync.Mutex
	path       string
	years      map[int]HolidayYear
	imports    map[int]HolidayYear
	attempted  map[int]time.Time
	failures   map[int]string
	stateError string
	refreshing bool
	generation uint64
	cancel     context.CancelFunc
	fetch      HolidayFetch
}

func NewHolidays(fetch HolidayFetch) *Holidays {
	if fetch == nil {
		fetch = fetchHolidayHTTP
	}
	return &Holidays{years: map[int]HolidayYear{}, imports: map[int]HolidayYear{}, attempted: map[int]time.Time{}, failures: map[int]string{}, fetch: fetch}
}

func fetchHolidayHTTP(ctx context.Context, url string) ([]byte, error) {
	// All URLs are fixed public holiday endpoints; no management or account
	// credentials enter this transport. DefaultTransport supports proxy env vars.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cpa-codex-5h-reset")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("holiday HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHolidayBytes+1))
	if len(raw) > maxHolidayBytes {
		return nil, errors.New("holiday data exceeds 2 MiB")
	}
	return raw, err
}

func holidayURLs(year int) []string {
	return []string{
		fmt.Sprintf("https://timor.tech/api/holiday/year/%d/", year),
		fmt.Sprintf("https://raw.githubusercontent.com/NateScarlet/holiday-cn/master/%d.json", year),
		fmt.Sprintf("https://cdn.jsdelivr.net/gh/NateScarlet/holiday-cn@master/%d.json", year),
	}
}

// ParseHolidayData accepts complete annual responses from both supported sources.
// expectedYear is zero for imports and fixed for remote downloads.
func ParseHolidayData(raw []byte, expectedYear int) (HolidayYear, error) {
	var result HolidayYear
	if len(raw) > maxHolidayBytes {
		return result, errors.New("holiday data exceeds 2 MiB")
	}
	var input struct {
		Year int  `json:"year"`
		Code *int `json:"code"`
		Days []struct {
			Date string `json:"date"`
			Name string `json:"name"`
			Off  *bool  `json:"isOffDay"`
		} `json:"days"`
		Holiday map[string]struct {
			Date string `json:"date"`
			Name string `json:"name"`
			Off  *bool  `json:"holiday"`
		} `json:"holiday"`
	}
	if json.Unmarshal(raw, &input) != nil || (input.Days != nil && input.Holiday != nil) {
		return result, errors.New("invalid holiday JSON")
	}
	add := func(date, name string, off *bool) error {
		t, err := parseDate(date)
		if err != nil || off == nil || strings.TrimSpace(name) == "" {
			return errors.New("holiday entries require a valid date, name and boolean day type")
		}
		// A following year's announcement can adjust the preceding December.
		if t.Year() != result.Year && !(t.Year() == result.Year-1 && t.Month() == time.December) {
			return errors.New("holiday entry does not belong to the declared year")
		}
		result.Days = append(result.Days, HolidayDay{date, strings.TrimSpace(name), *off})
		return nil
	}
	if input.Days != nil {
		result.Year = input.Year
		for _, day := range input.Days {
			if err := add(day.Date, day.Name, day.Off); err != nil {
				return result, err
			}
		}
	} else if input.Holiday != nil {
		if input.Code == nil || *input.Code != 0 {
			return result, errors.New("holiday source reported an error")
		}
		result.Year = expectedYear
		if result.Year == 0 {
			for _, day := range input.Holiday {
				t, err := parseDate(day.Date)
				if err != nil {
					return result, err
				}
				if result.Year != 0 && result.Year != t.Year() {
					return result, errors.New("annual holiday response contains multiple years")
				}
				result.Year = t.Year()
			}
		}
		for key, day := range input.Holiday {
			if len(day.Date) != 10 || (key != day.Date[5:] && key != day.Date) {
				return result, errors.New("holiday key and date disagree")
			}
			if err := add(day.Date, day.Name, day.Off); err != nil {
				return result, err
			}
		}
	} else {
		return result, errors.New("unsupported holiday JSON format")
	}
	if result.Year < 1900 || result.Year > 9998 || len(result.Days) == 0 || (expectedYear != 0 && expectedYear != result.Year) {
		return result, errors.New("holiday data is empty or has an invalid year")
	}
	seen := map[string]bool{}
	for _, day := range result.Days {
		if seen[day.Date] {
			return result, errors.New("duplicate or conflicting holiday dates")
		}
		seen[day.Date] = true
	}
	sort.Slice(result.Days, func(i, j int) bool { return result.Days[i].Date < result.Days[j].Date })
	return result, nil
}

func (h *Holidays) Configure(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.path == path {
		return
	}
	h.stopLocked()
	h.path = path
	h.years, h.imports = map[int]HolidayYear{}, map[int]HolidayYear{}
	h.attempted, h.failures = map[int]time.Time{}, map[int]string{}
	h.stateError = ""
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var state holidayDisk
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Schema != 1 {
		h.stateError = "节假日缓存读取异常，可刷新或手动导入节假日数据"
		return
	}
	for _, group := range []map[int]HolidayYear{state.Years, state.Imports} {
		for year, data := range group {
			if !validHolidayYear(year, data) {
				h.stateError = "节假日缓存读取异常，可刷新或手动导入节假日数据"
				return
			}
		}
	}
	if state.Years != nil {
		h.years = state.Years
	}
	if state.Imports != nil {
		h.imports = state.Imports
	}
}

func validHolidayYear(year int, data HolidayYear) bool {
	if data.Year != year || year < 1900 || year > 9998 || len(data.Days) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, day := range data.Days {
		t, err := parseDate(day.Date)
		if err != nil || strings.TrimSpace(day.Name) == "" || seen[day.Date] || (t.Year() != year && !(t.Year() == year-1 && t.Month() == time.December)) {
			return false
		}
		seen[day.Date] = true
	}
	return true
}

func (h *Holidays) stopLocked() {
	if h.cancel != nil {
		h.cancel()
	}
	h.generation++
	h.refreshing = false
}

func (h *Holidays) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLocked()
}

// Month grids always start on Monday and include six complete weeks.
func monthGridStart(month time.Time) time.Time {
	first := time.Date(month.Year(), month.Month(), 1, 12, 0, 0, 0, month.Location())
	return first.AddDate(0, 0, -(int(first.Weekday())+6)%7)
}

func calendarDisplayRange(local time.Time) (time.Time, time.Time) {
	month := time.Date(local.Year(), local.Month(), 1, 12, 0, 0, 0, local.Location())
	return monthGridStart(month.AddDate(0, -1, 0)), monthGridStart(month.AddDate(0, 1, 0)).AddDate(0, 0, 42)
}

func holidayYears(c Config, now time.Time) []int {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil
	}
	local := now.In(loc)
	start, end := calendarDisplayRange(local)
	years := []int{}
	for year := start.Year(); year <= end.AddDate(0, 0, -1).Year(); year++ {
		years = append(years, year)
	}
	return years
}

func (h *Holidays) Refresh(c Config, now time.Time, force bool) bool {
	h.mu.Lock()
	if h.refreshing {
		h.mu.Unlock()
		return false
	}
	years := []int{}
	for _, year := range holidayYears(c, now) {
		if _, manual := h.imports[year]; manual {
			continue
		}
		data, cached := h.years[year]
		if !force {
			if last := h.attempted[year]; !last.IsZero() && now.Sub(last) < 30*time.Minute {
				continue
			}
			if cached && h.failures[year] == "" && now.Sub(data.UpdatedAt) < 24*time.Hour {
				continue
			}
		}
		years = append(years, year)
	}
	if len(years) == 0 {
		h.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.generation++
	generation := h.generation
	h.refreshing = true
	h.mu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			h.mu.Lock()
			if h.generation == generation {
				h.refreshing = false
			}
			h.mu.Unlock()
		}()
		for _, year := range years {
			var data HolidayYear
			found := false
			for _, url := range holidayURLs(year) {
				if ctx.Err() != nil {
					return
				}
				raw, err := h.fetch(ctx, url)
				if err != nil {
					continue
				}
				data, err = ParseHolidayData(raw, year)
				if err != nil {
					continue
				}
				data.Source, data.UpdatedAt = url, now
				found = true
				break
			}
			h.mu.Lock()
			if h.generation != generation || ctx.Err() != nil {
				h.mu.Unlock()
				return
			}
			h.attempted[year] = now
			if found {
				h.years[year] = data
				delete(h.failures, year)
				if h.saveLocked(h.imports) != nil {
					h.stateError = "节假日缓存写入失败，当前数据仅在本次运行中有效"
				} else {
					h.stateError = ""
				}
			} else {
				h.failures[year] = "节假日数据获取异常"
			}
			h.mu.Unlock()
		}
	}()
	return true
}

func (h *Holidays) saveLocked(imports map[int]HolidayYear) error {
	raw, err := json.Marshal(holidayDisk{1, h.years, imports})
	if err != nil {
		return err
	}
	return writeAtomic(h.path, raw, ".holidays-*.tmp")
}

func writeAtomic(path string, raw []byte, pattern string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), pattern)
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

func (h *Holidays) Import(data HolidayYear, now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !validHolidayYear(data.Year, data) {
		return errors.New("invalid holiday data")
	}
	imports := make(map[int]HolidayYear, len(h.imports)+1)
	for year, value := range h.imports {
		imports[year] = value
	}
	data.Source, data.UpdatedAt = "manual", now
	data.Days = append([]HolidayDay{}, data.Days...)
	imports[data.Year] = data
	if h.saveLocked(imports) != nil {
		return errors.New("holiday_write_failed")
	}
	h.imports = imports
	h.stateError = ""
	return nil
}

func (h *Holidays) ClearImport(year int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	imports := make(map[int]HolidayYear, len(h.imports))
	for y, value := range h.imports {
		if y != year {
			imports[y] = value
		}
	}
	if h.saveLocked(imports) != nil {
		return errors.New("holiday_write_failed")
	}
	h.imports = imports
	h.stateError = ""
	return nil
}

func (h *Holidays) classify(local time.Time) CalendarDay {
	date := local.Format("2006-01-02")
	day := CalendarDay{Date: date, Kind: "weekday", Name: "工作日"}
	if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
		day.Kind, day.Name = "weekend", "普通周末"
	}
	if h == nil {
		return day
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, online := h.years[local.Year()]
	_, manual := h.imports[local.Year()]
	day.DataKnown = online || manual
	for index, group := range []map[int]HolidayYear{h.imports, h.years} {
		for _, year := range []int{local.Year() + 1, local.Year()} {
			if _, manual := h.imports[year]; index == 1 && manual {
				continue
			}
			for _, record := range group[year].Days {
				if record.Date == date {
					day.Name, day.DataKnown = record.Name, true
					day.Kind = "workday"
					if record.IsOffDay {
						day.Kind = "holiday"
					}
					return day
				}
			}
		}
	}
	return day
}

func DateBlockReason(c Config, at time.Time, h *Holidays) string {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return "invalid_timezone"
	}
	local := at.In(loc)
	date, rule := local.Format("2006-01-02"), c.OneTimeRule
	if date >= rule.StartDate && date <= rule.EndDate {
		if rule.Mode == "exclude" {
			return "excluded_date"
		}
		if rule.Mode == "wake_tomorrow" {
			return ""
		}
	}
	if c.SkipCNHolidays {
		day := h.classify(local)
		if day.Kind == "holiday" || day.Kind == "weekend" {
			return day.Kind
		}
	}
	return ""
}

func (h *Holidays) yearStatusLocked(year int) HolidayYearStatus {
	data, known := h.years[year]
	manual := false
	if imported, ok := h.imports[year]; ok {
		data, known, manual = imported, true, true
	}
	out := HolidayYearStatus{Year: year, Source: data.Source, UpdatedAt: data.UpdatedAt, Known: known, Manual: manual}
	for _, day := range data.Days {
		if day.IsOffDay {
			out.OffDays++
		} else {
			out.Workdays++
		}
	}
	if !manual && h.failures[year] != "" {
		out.Error = "节假日数据获取异常，可手动导入节假日数据"
		if known {
			out.Error = "节假日数据获取异常，正在使用缓存"
		}
	}
	return out
}

func HolidaySummary(data HolidayYear) HolidayYearStatus {
	out := HolidayYearStatus{Year: data.Year, Known: true}
	for _, day := range data.Days {
		if day.IsOffDay {
			out.OffDays++
		} else {
			out.Workdays++
		}
	}
	return out
}

func (h *Holidays) Calendar(c Config, now time.Time) HolidayCalendar {
	out := HolidayCalendar{Timezone: c.Timezone, Now: now, Days: []CalendarDay{}, DisplayDays: []CalendarDay{}, Years: []HolidayYearStatus{}}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return out
	}
	local := now.In(loc)
	for i := 0; i < 30; i++ {
		at := time.Date(local.Year(), local.Month(), local.Day()+i, 12, 0, 0, 0, loc)
		day := h.classify(at)
		day.Reason = DateBlockReason(c, at, h)
		day.Skip = day.Reason != ""
		out.Days = append(out.Days, day)
	}
	// Keep the next-30-day API intact while supplying complete previous,
	// current and next month grids, including their adjacent-month dates.
	start, end := calendarDisplayRange(local)
	for at := start; at.Before(end); at = at.AddDate(0, 0, 1) {
		day := h.classify(at)
		day.Reason = DateBlockReason(c, at, h)
		day.Skip = day.Reason != ""
		out.DisplayDays = append(out.DisplayDays, day)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out.Refreshing, out.Error = h.refreshing, h.stateError
	years := holidayYears(c, now)
	// Keep imported years visible so the user can clear any imported file.
	for year := range h.imports {
		found := false
		for _, y := range years {
			found = found || y == year
		}
		if !found {
			years = append(years, year)
		}
	}
	sort.Ints(years)
	for _, year := range years {
		status := h.yearStatusLocked(year)
		out.Years = append(out.Years, status)
		if status.Error != "" && out.Error == "" {
			out.Error = strconv.Itoa(year) + " 年：" + status.Error
		}
	}
	return out
}
