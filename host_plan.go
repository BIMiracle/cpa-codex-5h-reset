package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

func credentialPlan(raw []byte) string {
	var credential struct {
		JSON struct {
			PlanType string `json:"plan_type"`
			IDToken  string `json:"id_token"`
		} `json:"json"`
	}
	if json.Unmarshal(raw, &credential) != nil {
		return ""
	}
	// Match CPA's Codex plan source: the auth claim in the stored ID token.
	// This reads metadata only; token authentication remains the host's job.
	parts := strings.Split(strings.TrimSpace(credential.JSON.IDToken), ".")
	if len(parts) == 3 {
		payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		var claims struct {
			Auth struct {
				PlanType string `json:"chatgpt_plan_type"`
			} `json:"https://api.openai.com/auth"`
		}
		if err == nil && json.Unmarshal(payload, &claims) == nil {
			if plan := strings.ToLower(strings.TrimSpace(claims.Auth.PlanType)); plan != "" {
				return plan
			}
		}
	}
	return strings.ToLower(strings.TrimSpace(credential.JSON.PlanType))
}
