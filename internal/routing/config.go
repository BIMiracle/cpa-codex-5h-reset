package routing

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Binding struct {
	Key   string   `yaml:"key"`
	Allow []string `yaml:"allow"`
}

type Config struct {
	Enabled       bool      `yaml:"enabled"`
	SessionTTL    string    `yaml:"session_ttl"`
	QuotaCacheTTL string    `yaml:"quota_cache_ttl"`
	StateFile     string    `yaml:"state_file"`
	Bindings      []Binding `yaml:"bindings"`
	Unbound       string    `yaml:"unbound"`
	sessionTTL    time.Duration
	quotaTTL      time.Duration
}

// Parse keeps routing independent of wake schedules and their account selection.
func Parse(raw []byte, wakeStateFile string) (Config, error) {
	var document struct {
		Routing Config `yaml:"quota_routing"`
	}
	document.Routing = Config{SessionTTL: "4h", QuotaCacheTTL: "1m", Unbound: "passthrough"}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return Config{}, fmt.Errorf("invalid quota_routing configuration")
	}
	c := document.Routing
	var err error
	c.sessionTTL, err = time.ParseDuration(c.SessionTTL)
	if err != nil || c.sessionTTL < time.Minute || c.sessionTTL > 7*24*time.Hour {
		return Config{}, fmt.Errorf("quota_routing.session_ttl must be 1m..168h")
	}
	c.quotaTTL, err = time.ParseDuration(c.QuotaCacheTTL)
	if err != nil || c.quotaTTL < time.Second || c.quotaTTL > 5*time.Minute {
		return Config{}, fmt.Errorf("quota_routing.quota_cache_ttl must be 1s..5m")
	}
	if c.StateFile == "" {
		c.StateFile = wakeStateFile + ".routing.json"
	}
	c.StateFile, err = filepath.Abs(c.StateFile)
	if err != nil {
		return Config{}, fmt.Errorf("invalid quota_routing.state_file")
	}
	if c.Unbound != "passthrough" && c.Unbound != "deny" {
		return Config{}, fmt.Errorf("quota_routing.unbound must be passthrough or deny")
	}
	keys := map[string]bool{}
	for i := range c.Bindings {
		b := &c.Bindings[i]
		b.Key = strings.TrimSpace(b.Key)
		if b.Key == "" || keys[b.Key] || len(b.Allow) == 0 {
			return Config{}, fmt.Errorf("quota_routing.bindings[%d]: unique key and allow patterns required", i)
		}
		keys[b.Key] = true
		for j, pattern := range b.Allow {
			pattern = strings.ToLower(strings.TrimSpace(pattern))
			if _, err := path.Match(pattern, "auth.json"); err != nil || pattern == "" {
				return Config{}, fmt.Errorf("quota_routing.bindings[%d]: invalid allow pattern", i)
			}
			b.Allow[j] = pattern
		}
	}
	return c, nil
}
