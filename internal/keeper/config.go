package keeper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"
)

type Schedule struct {
	ID      string `yaml:"id" json:"id"`
	At      string `yaml:"at" json:"at"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

type OneTimeRule struct {
	Mode      string `yaml:"mode" json:"mode"`
	StartDate string `yaml:"start_date,omitempty" json:"start_date,omitempty"`
	EndDate   string `yaml:"end_date,omitempty" json:"end_date,omitempty"`
}

func (r OneTimeRule) Effective(now time.Time, timezone string) OneTimeRule {
	loc, err := time.LoadLocation(timezone)
	if err != nil || r.Mode == "" || r.Mode == "none" || r.EndDate < now.In(loc).Format("2006-01-02") {
		return OneTimeRule{Mode: "none"}
	}
	return r
}

func parseDate(value string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", value)
	if err != nil || t.Format("2006-01-02") != value {
		return time.Time{}, fmt.Errorf("date must be YYYY-MM-DD")
	}
	return t, nil
}

type Config struct {
	Enabled              bool        `yaml:"enabled" json:"enabled"`
	Model                string      `yaml:"model" json:"model"`
	ReasoningEffort      string      `yaml:"reasoning_effort" json:"reasoning_effort"`
	Prompt               string      `yaml:"prompt" json:"prompt"`
	Timezone             string      `yaml:"timezone" json:"timezone"`
	SkipCNHolidays       bool        `yaml:"skip_cn_holidays" json:"skip_cn_holidays"`
	OneTimeRule          OneTimeRule `yaml:"one_time_rule" json:"one_time_rule"`
	Schedules            []Schedule  `yaml:"schedules" json:"schedules"`
	AuthIDs              []string    `yaml:"auth_ids" json:"auth_ids"`
	StateFile            string      `yaml:"state_file" json:"state_file"`
	MaxRetries           int         `yaml:"max_retries" json:"max_retries"`
	DesktopNotifications bool        `yaml:"desktop_notifications" json:"desktop_notifications"`
	MaxLogEntries        int         `yaml:"max_log_entries" json:"max_log_entries"`
	Times                []string    `yaml:"times,omitempty" json:"times,omitempty"`
}

func Defaults() Config {
	root, err := os.UserConfigDir()
	if err != nil {
		root = "plugins"
	}
	return Config{Model: "gpt-6-luna", ReasoningEffort: "low", Prompt: "Reply with OK.", Timezone: "Asia/Singapore", OneTimeRule: OneTimeRule{Mode: "none"}, MaxRetries: 3, MaxLogEntries: 200,
		StateFile: filepath.Join(root, "CLIProxyAPI", "state", "cpa-codex-5h-reset.json"),
		Schedules: []Schedule{{"morning", "05:00", true}, {"midday", "10:01", true}, {"afternoon", "15:02", true}, {"evening", "20:03", true}}}
}
func (c *Config) Validate() error {
	c.Model = strings.TrimSpace(c.Model)
	c.Prompt = strings.TrimSpace(c.Prompt)
	if c.Model == "" || c.Prompt == "" {
		return fmt.Errorf("model and prompt are required")
	}
	switch c.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("invalid reasoning_effort")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone")
	}
	r := &c.OneTimeRule
	switch r.Mode {
	case "", "none":
		*r = OneTimeRule{Mode: "none"}
	case "exclude", "wake_tomorrow":
		start, err := parseDate(r.StartDate)
		if err != nil {
			return fmt.Errorf("invalid one_time_rule start_date")
		}
		end, err := parseDate(r.EndDate)
		if err != nil || end.Before(start) || end.Sub(start) > 29*24*time.Hour {
			return fmt.Errorf("one_time_rule must cover 1..30 calendar days")
		}
		if r.Mode == "wake_tomorrow" && r.StartDate != r.EndDate {
			return fmt.Errorf("wake_tomorrow must specify one fixed date")
		}
	default:
		return fmt.Errorf("invalid one_time_rule mode")
	}
	if c.Times != nil {
		c.Schedules = nil
		for i, at := range c.Times {
			c.Schedules = append(c.Schedules, Schedule{fmt.Sprintf("legacy-%d", i), at, true})
		}
		c.Times = nil
	}
	if len(c.Schedules) > 24 {
		return fmt.Errorf("at most 24 schedules")
	}
	ids, times := map[string]bool{}, map[string]bool{}
	for _, s := range c.Schedules {
		if s.ID == "" || ids[s.ID] {
			return fmt.Errorf("schedule IDs must be nonempty and unique")
		}
		ids[s.ID] = true
		t, err := time.Parse("15:04", s.At)
		if err != nil || t.Format("15:04") != s.At {
			return fmt.Errorf("schedule time must be HH:MM")
		}
		if s.Enabled && times[s.At] {
			return fmt.Errorf("duplicate enabled schedule time")
		}
		if s.Enabled {
			times[s.At] = true
		}
	}
	if c.MaxRetries < 0 || c.MaxRetries > 120 {
		return fmt.Errorf("max_retries must be 0..120")
	}
	if c.MaxLogEntries < 1 || c.MaxLogEntries > 2000 {
		return fmt.Errorf("max_log_entries must be 1..2000")
	}
	if strings.TrimSpace(c.StateFile) == "" {
		return fmt.Errorf("state_file is required")
	}
	path, err := filepath.Abs(c.StateFile)
	if err != nil {
		return err
	}
	c.StateFile = path
	return nil
}
func (c Config) Selects(id string) bool {
	if len(c.AuthIDs) == 0 {
		return true
	}
	for _, v := range c.AuthIDs {
		if v == id {
			return true
		}
	}
	return false
}
func (c Config) Clone() Config {
	c.Schedules = append([]Schedule{}, c.Schedules...)
	c.AuthIDs = append([]string{}, c.AuthIDs...)
	return c
}

type ScheduledTime struct {
	ID   string    `json:"id"`
	At   string    `json:"at"`
	Time time.Time `json:"time"`
}

func NextRuns(c Config, now time.Time, calendars ...*Holidays) []ScheduledTime {
	loc, _ := time.LoadLocation(c.Timezone)
	out := []ScheduledTime{}
	if loc == nil {
		return out
	}
	local := now.In(loc)
	var holidays *Holidays
	if len(calendars) > 0 {
		holidays = calendars[0]
	}
	for _, s := range c.Schedules {
		if !s.Enabled {
			continue
		}
		t, _ := time.Parse("15:04", s.At)
		for day := 0; day < 366; day++ {
			due := time.Date(local.Year(), local.Month(), local.Day()+day, t.Hour(), t.Minute(), 0, 0, loc)
			if due.After(now) && DateBlockReason(c, due, holidays) == "" {
				out = append(out, ScheduledTime{s.ID, s.At, due})
				break
			}
		}
	}
	return out
}
