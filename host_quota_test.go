package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
)

func TestQuotaOAuthRecovery(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		expiring, alwaysUnauthorized bool
		refreshError, wantError      string
		wantRequests, wantRefreshes  int
	}{
		{name: "healthy", wantRequests: 1},
		{name: "401 recovery", wantRequests: 2, wantRefreshes: 1},
		{name: "early renewal", expiring: true, wantRequests: 1, wantRefreshes: 1},
		{name: "persistent 401", alwaysUnauthorized: true, wantError: "quota_unauthorized", wantRequests: 2, wantRefreshes: 1},
		{name: "invalid refresh token", refreshError: "credential_reauth_required", wantError: "credential_reauth_required", wantRequests: 1, wantRefreshes: 1},
		{name: "old host", refreshError: "unsupported", wantError: "credential_refresh_unavailable", wantRequests: 1, wantRefreshes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := invokeHost
			defer func() { invokeHost = old }()
			now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
			requests, refreshes := 0, 0
			invokeHost = func(method string, input any) (json.RawMessage, error) {
				m := input.(map[string]any)
				switch method {
				case "host.auth.get":
					c := quotaCredential{AccessToken: "old-secret", AccountID: "business-account"}
					if tc.expiring {
						c.Expired = now.Add(time.Minute).Format(time.RFC3339)
					}
					return json.Marshal(map[string]any{"json": c})
				case "host.auth.refresh":
					refreshes++
					if m["auth_index"] != "business-index" || m["access_token"] != "old-secret" {
						t.Fatal("refresh identity mismatch")
					}
					if tc.refreshError == "unsupported" {
						return nil, errors.New("unsupported secret")
					}
					if tc.refreshError != "" {
						return json.Marshal(map[string]string{"error": tc.refreshError})
					}
					return json.Marshal(map[string]any{"json": quotaCredential{AccessToken: "new-secret", AccountID: "business-account"}})
				case "host.http.do":
					requests++
					if m["Method"] != "GET" || m["URL"] != "https://chatgpt.com/backend-api/wham/usage" {
						t.Fatal("unexpected quota destination")
					}
					h := m["Headers"].(http.Header)
					if h.Get("ChatGPT-Account-Id") != "business-account" {
						t.Fatal("lost account context")
					}
					code := 200
					if tc.alwaysUnauthorized || (tc.name != "healthy" && h.Get("Authorization") == "Bearer old-secret") {
						code = 401
					}
					return json.Marshal(map[string]any{"StatusCode": code, "Body": []byte(`{"rate_limit":{"primary_window":{"limit_window_seconds":604800,"used_percent":9,"reset_after_seconds":600}}}`)})
				default:
					t.Fatalf("quota recovery must not invoke %s", method)
					return nil, nil
				}
			}
			q, err := (host{}).FetchQuota(keeper.Auth{ID: "business", AuthIndex: "business-index"}, now)
			if q.Error != tc.wantError || (err != nil) != (tc.wantError != "") || requests != tc.wantRequests || refreshes != tc.wantRefreshes {
				t.Fatalf("quota=%+v err=%v requests=%d refreshes=%d", q, err, requests, refreshes)
			}
			if err == nil && (q.Weekly == nil || q.Weekly.UsedPercent != 9 || q.Known) {
				t.Fatal("weekly Business quota lost")
			}
			data, _ := json.Marshal(q)
			if strings.Contains(string(data), "secret") {
				t.Fatal("token leaked in quota result")
			}
		})
	}
}

func TestQuotaExpiryUsesAccessTokenNotIDToken(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-time.Minute, 5 * time.Minute, 10 * 24 * time.Hour} {
		payload, _ := json.Marshal(map[string]int64{"exp": now.Add(offset).Unix()})
		c := quotaCredential{AccessToken: "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s", Expired: now.Add(-time.Hour).Format(time.RFC3339)}
		if c.expiresSoon(now) != (offset <= 5*time.Minute) {
			t.Fatal("JWT expiry precedence lost")
		}
	}
}
