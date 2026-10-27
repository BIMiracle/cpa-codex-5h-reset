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
	Known       bool         `json:"known"`
	ResetAt     time.Time    `json:"reset_at"`
	UsedPercent float64      `json:"used_percent"`
	ObservedAt  time.Time    `json:"observed_at"`
	Error       string       `json:"error,omitempty"`
	Weekly      *QuotaWindow `json:"weekly,omitempty"`
}

type QuotaWindow struct {
	ResetAt     time.Time `json:"reset_at"`
	UsedPercent float64   `json:"used_percent"`
}

// Match actual durations: some plans expose only a weekly primary window.
// Known, ResetAt and UsedPercent continue to describe the five-hour window.
func ParseQuota(raw []byte, now time.Time) (Quota, error) {
	var doc struct {
		RateLimit struct {
			Primary   window `json:"primary_window"`
			Secondary window `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	q := Quota{ObservedAt: now}
	if json.Unmarshal(raw, &doc) != nil {
		q.Error = "quota_unknown"
		return q, errors.New("invalid quota response")
	}
	for _, w := range []window{doc.RateLimit.Primary, doc.RateLimit.Secondary} {
		if w.Seconds != 18000 && w.Seconds != 604800 {
			continue
		}
		reset := w.ResetAt
		if reset == 0 && w.After != nil && *w.After >= 0 {
			reset = now.Unix() + *w.After
		}
		if reset <= 0 || w.Used < 0 || w.Used > 100 {
			continue
		}
		if w.Seconds == 18000 {
			q.Known, q.ResetAt, q.UsedPercent = true, time.Unix(reset, 0), w.Used
		} else {
			q.Weekly = &QuotaWindow{ResetAt: time.Unix(reset, 0), UsedPercent: w.Used}
		}
	}
	if q.Known || q.Weekly != nil {
		return q, nil
	}
	q.Error = "quota_unknown"
	return q, errors.New("quota windows unavailable")
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
