package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
)

type quotaCredential struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
	Expired     string `json:"expired"`
}

func quotaFailure(now time.Time, code string) (keeper.Quota, error) {
	return keeper.Quota{ObservedAt: now, Error: code}, errors.New(code)
}

func (c quotaCredential) expiresSoon(now time.Time) bool {
	// JWT metadata is used only to schedule renewal, never to authenticate a token.
	parts := strings.Split(c.AccessToken, ".")
	if len(parts) == 3 {
		payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if err == nil && json.Unmarshal(payload, &claims) == nil && claims.Exp > 0 {
			return !time.Unix(claims.Exp, 0).After(now.Add(5 * time.Minute))
		}
	}
	expiry, err := time.Parse(time.RFC3339, c.Expired)
	return err == nil && !expiry.After(now.Add(5*time.Minute))
}

func refreshQuotaCredential(a keeper.Auth, current quotaCredential) (quotaCredential, string) {
	// The host serializes refreshes with model requests and persists rotated tokens.
	raw, err := invokeHost("host.auth.refresh", map[string]any{"auth_index": a.AuthIndex, "access_token": current.AccessToken})
	if err != nil {
		return current, "credential_refresh_unavailable"
	}
	var response struct {
		JSON  quotaCredential `json:"json"`
		Error string          `json:"error"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return current, "credential_refresh_failed"
	}
	if response.Error == "credential_reauth_required" {
		return current, response.Error
	}
	if response.Error != "" || response.JSON.AccessToken == "" {
		return current, "credential_refresh_failed"
	}
	return response.JSON, ""
}

func (host) FetchQuota(a keeper.Auth, now time.Time) (keeper.Quota, error) {
	if a.AuthIndex == "" {
		return quotaFailure(now, "auth_index_missing")
	}
	raw, err := invokeHost("host.auth.get", map[string]any{"auth_index": a.AuthIndex})
	var stored struct {
		JSON quotaCredential `json:"json"`
	}
	if err != nil || json.Unmarshal(raw, &stored) != nil || stored.JSON.AccessToken == "" {
		return quotaFailure(now, "credential_unavailable")
	}
	credential := stored.JSON
	attemptedRefresh, refreshError := false, ""
	if credential.expiresSoon(now) {
		attemptedRefresh = true
		credential, refreshError = refreshQuotaCredential(a, credential)
	}
	for {
		headers := http.Header{"Authorization": {"Bearer " + credential.AccessToken}, "Accept": {"application/json"}, "User-Agent": {"cpa-codex-5h-reset"}}
		if credential.AccountID != "" {
			headers.Set("ChatGPT-Account-Id", credential.AccountID)
		}
		// Fixed official origin: credential secrets never go to a configurable URL.
		raw, err = invokeHost("host.http.do", map[string]any{"Method": "GET", "URL": "https://chatgpt.com/backend-api/wham/usage", "Headers": headers})
		var response struct {
			StatusCode int
			Body       []byte
		}
		if err != nil || json.Unmarshal(raw, &response) != nil {
			return quotaFailure(now, "quota_request_failed")
		}
		if response.StatusCode == http.StatusOK {
			return keeper.ParseQuota(response.Body, now)
		}
		if response.StatusCode != http.StatusUnauthorized {
			return quotaFailure(now, "quota_request_failed")
		}
		if attemptedRefresh {
			if refreshError != "" {
				return quotaFailure(now, refreshError)
			}
			return quotaFailure(now, "quota_unauthorized")
		}
		attemptedRefresh = true
		credential, refreshError = refreshQuotaCredential(a, credential)
		if refreshError != "" {
			return quotaFailure(now, refreshError)
		}
	}
}
