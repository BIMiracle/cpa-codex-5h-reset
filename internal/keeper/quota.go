package keeper

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type WindowDecision string

const (
	WindowFresh       WindowDecision = "fresh_window"
	WindowWait        WindowDecision = "wait_for_reset"
	WindowAlreadyOpen WindowDecision = "already_active"
	WindowUnknown     WindowDecision = "request_ok_reset_unknown"
)

// ResetAt reads the per-credential signal returned by CPA's internal model
// execution. No client-facing passthrough-headers setting is required.
func ResetAt(headers http.Header, now time.Time) (time.Time, bool) {
	if raw := headerValue(headers, "X-Codex-Primary-Reset-At"); raw != "" {
		if epoch, err := strconv.ParseInt(raw, 10, 64); err == nil && epoch > 0 {
			return time.Unix(epoch, 0), true
		}
	}
	if raw := headerValue(headers, "X-Codex-Primary-Reset-After-Seconds"); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
			return now.Add(time.Duration(seconds) * time.Second), true
		}
	}
	return time.Time{}, false
}

func headerValue(headers http.Header, name string) string {
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func DecideWindow(resetAt time.Time, hasReset bool, now, due, deadline time.Time) WindowDecision {
	if !hasReset || !resetAt.After(now) {
		return WindowUnknown
	}
	// A reset about five hours away suggests a newly opened primary window.
	if resetAt.Sub(now) >= 285*time.Minute {
		return WindowFresh
	}
	if !resetAt.After(deadline) && resetAt.After(due) {
		return WindowWait
	}
	return WindowAlreadyOpen
}

func RetryDelay(cfg Config, attempt int) time.Duration {
	seconds := cfg.RetryInitialSeconds
	for n := 1; n < attempt && seconds < cfg.RetryMaxSeconds; n++ {
		seconds *= 2
		if seconds > cfg.RetryMaxSeconds {
			seconds = cfg.RetryMaxSeconds
		}
	}
	return time.Duration(seconds) * time.Second
}
