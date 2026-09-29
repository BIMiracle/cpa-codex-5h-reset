package keeper

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Quota struct {
	Known       bool      `json:"known"`
	ResetAt     time.Time `json:"reset_at"`
	UsedPercent float64   `json:"used_percent"`
	ObservedAt  time.Time `json:"observed_at"`
	Error       string    `json:"error,omitempty"`
}

// Match the actual duration, never assume primary means five hours.
func ParseQuota(raw []byte, now time.Time) (Quota, error) {
	var doc struct {
		RateLimit struct {
			Primary   window `json:"primary_window"`
			Secondary window `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	unknown := Quota{ObservedAt: now, Error: "quota_unknown"}
	if json.Unmarshal(raw, &doc) != nil {
		return unknown, errors.New("invalid quota response")
	}
	for _, w := range []window{doc.RateLimit.Primary, doc.RateLimit.Secondary} {
		if w.Seconds != 18000 {
			continue
		}
		reset := w.ResetAt
		if reset == 0 && w.After != nil && *w.After >= 0 {
			reset = now.Unix() + *w.After
		}
		if reset <= 0 || w.Used < 0 || w.Used > 100 {
			continue
		}
		return Quota{Known: true, ResetAt: time.Unix(reset, 0), UsedPercent: w.Used, ObservedAt: now}, nil
	}
	return unknown, errors.New("five hour window unavailable")
}

type window struct {
	Seconds int64   `json:"limit_window_seconds"`
	ResetAt int64   `json:"reset_at"`
	After   *int64  `json:"reset_after_seconds"`
	Used    float64 `json:"used_percent"`
}

func ResetAt(headers http.Header, now time.Time) (time.Time, bool) {
	for _, h := range []struct {
		Name     string
		Relative bool
	}{{"X-Codex-Primary-Reset-At", false}, {"X-Codex-Primary-Reset-After-Seconds", true}} {
		for key, values := range headers {
			if !strings.EqualFold(key, h.Name) || len(values) == 0 {
				continue
			}
			n, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || n <= 0 {
				continue
			}
			if h.Relative {
				return now.Add(time.Duration(n) * time.Second), true
			}
			return time.Unix(n, 0), true
		}
	}
	return time.Time{}, false
}
