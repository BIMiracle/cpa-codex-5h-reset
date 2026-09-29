package keeper

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	Enabled             bool     `yaml:"enabled" json:"enabled"`
	Model               string   `yaml:"model" json:"model"`
	ReasoningEffort     string   `yaml:"reasoning_effort" json:"reasoning_effort"`
	Prompt              string   `yaml:"prompt" json:"prompt"`
	Timezone            string   `yaml:"timezone" json:"timezone"`
	Times               []string `yaml:"times" json:"times"`
	StateFile           string   `yaml:"state_file" json:"state_file"`
	RetryAttempts       int      `yaml:"retry_attempts" json:"retry_attempts"`
	RetryInitialSeconds int      `yaml:"retry_initial_seconds" json:"retry_initial_seconds"`
	RetryMaxSeconds     int      `yaml:"retry_max_seconds" json:"retry_max_seconds"`
	MaxDelayMinutes     int      `yaml:"max_delay_minutes" json:"max_delay_minutes"`
	GraceSeconds        int      `yaml:"grace_seconds" json:"grace_seconds"`
	NotificationScript  string   `yaml:"notification_script" json:"notification_script,omitempty"`
}

func Defaults() Config {
	return Config{
		Model:               "gpt-6-luna",
		ReasoningEffort:     "low",
		Prompt:              "Reply with OK.",
		Timezone:            "Asia/Singapore",
		Times:               []string{"05:00", "10:01", "15:02", "20:03"},
		StateFile:           filepath.Join("plugins", "state", "cpa-codex-window-keeper.json"),
		RetryAttempts:       50,
		RetryInitialSeconds: 15,
		RetryMaxSeconds:     180,
		MaxDelayMinutes:     180,
		GraceSeconds:        5,
	}
}

func (c *Config) Validate() error {
	c.Model = strings.TrimSpace(c.Model)
	c.ReasoningEffort = strings.TrimSpace(c.ReasoningEffort)
	c.Prompt = strings.TrimSpace(c.Prompt)
	c.Timezone = strings.TrimSpace(c.Timezone)
	c.StateFile = strings.TrimSpace(c.StateFile)
	c.NotificationScript = strings.TrimSpace(c.NotificationScript)
	if c.Model == "" || c.Prompt == "" {
		return fmt.Errorf("model and prompt are required")
	}
	switch c.ReasoningEffort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("reasoning_effort must be low, medium, high, xhigh, or max")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	if len(c.Times) == 0 || len(c.Times) > 24 {
		return fmt.Errorf("times must contain between 1 and 24 entries")
	}
	seen := map[string]bool{}
	for _, at := range c.Times {
		if _, err := time.Parse("15:04", at); err != nil {
			return fmt.Errorf("invalid time %q: use HH:MM", at)
		}
		if seen[at] {
			return fmt.Errorf("duplicate time %q", at)
		}
		seen[at] = true
	}
	if c.RetryAttempts < 1 || c.RetryAttempts > 120 {
		return fmt.Errorf("retry_attempts must be 1..120")
	}
	if c.RetryInitialSeconds < 1 || c.RetryInitialSeconds > 600 ||
		c.RetryMaxSeconds < c.RetryInitialSeconds || c.RetryMaxSeconds > 1800 {
		return fmt.Errorf("invalid retry seconds")
	}
	if c.MaxDelayMinutes < 1 || c.MaxDelayMinutes > 300 {
		return fmt.Errorf("max_delay_minutes must be 1..300")
	}
	if c.GraceSeconds < 0 || c.GraceSeconds > 60 {
		return fmt.Errorf("grace_seconds must be 0..60")
	}
	if c.StateFile == "" {
		return fmt.Errorf("state_file is required")
	}
	path, err := filepath.Abs(c.StateFile)
	if err != nil {
		return fmt.Errorf("state_file: %w", err)
	}
	c.StateFile = path
	if c.NotificationScript != "" && !filepath.IsAbs(c.NotificationScript) {
		return fmt.Errorf("notification_script must be an absolute path")
	}
	return nil
}

func DueTimes(c Config, now time.Time) []time.Time {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil
	}
	local := now.In(loc)
	out := make([]time.Time, 0, len(c.Times))
	for _, at := range c.Times {
		clock, err := time.Parse("15:04", at)
		if err != nil {
			continue
		}
		due := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
		if due.After(now) {
			due = due.AddDate(0, 0, -1)
		}
		out = append(out, due)
	}
	return out
}
