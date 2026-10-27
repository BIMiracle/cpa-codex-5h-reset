package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func planCredential(plan string) json.RawMessage {
	claims, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": plan}})
	token := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	raw, _ := json.Marshal(map[string]any{"json": map[string]string{"id_token": token, "access_token": "secret-token"}})
	return raw
}

func TestCredentialPlan(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{string(planCredential(" Plus ")), "plus"},
		{string(planCredential("business_premium")), "business_premium"},
		{`{"json":{"plan_type":"Business Premium"}}`, "business premium"},
		{`{"json":{"plan_type":"plus","id_token":"invalid"}}`, "plus"},
		{`{"json":{"id_token":"bad.%%%.token"}}`, ""},
		{`{"json":{"id_token":"bad.e30.token"}}`, ""},
		{`{"json":{"access_token":"secret-token"}}`, ""},
		{`{"json":{"id_token":{}}}`, ""},
		{`invalid`, ""},
	} {
		if got := credentialPlan([]byte(tc.raw)); got != tc.want {
			t.Fatalf("expected %q, got %q", tc.want, got)
		}
	}
}

func TestListAuthsResolvesPlansWithoutExposingCredentials(t *testing.T) {
	old := invokeHost
	defer func() { invokeHost = old }()
	reads := 0
	invokeHost = func(method string, input any) (json.RawMessage, error) {
		switch method {
		case "host.auth.list":
			return json.RawMessage(`{"files":[
				{"id":"plus","provider":"codex","auth_index":"plus"},
				{"id":"business","provider":"codex","auth_index":"business"},
				{"id":"unavailable","provider":"codex","auth_index":"unavailable"},
				{"id":"unknown","provider":"codex","auth_index":"unknown"},
				{"id":"missing","provider":"codex"},
				{"id":"disabled","provider":"codex","disabled":true,"auth_index":"disabled"},
				{"id":"other","provider":"claude","auth_index":"other"},
				{"id":"direct","provider":"codex","plan_type":" PLUS ","auth_index":"direct"}
			]}`), nil
		case "host.auth.get":
			reads++
			switch input.(map[string]any)["auth_index"] {
			case "plus":
				return planCredential("plus"), nil
			case "business":
				return planCredential("business_premium"), nil
			case "unavailable":
				return nil, errors.New("secret error")
			case "unknown":
				return json.RawMessage(`{"json":{"access_token":"secret-token"}}`), nil
			}
		}
		t.Errorf("unexpected callback %s", method)
		return nil, errors.New("unexpected callback")
	}
	auths, err := (host{}).ListAuths()
	if err != nil || len(auths) != 8 || reads != 4 {
		t.Fatalf("list: auths=%+v err=%v reads=%d", auths, err, reads)
	}
	for i, want := range []string{"plus", "business_premium", "", "", "", "", "", "plus"} {
		if auths[i].PlanType != want || auths[i].IsPlus() != (want == "plus") {
			t.Fatalf("account %d: %+v", i, auths[i])
		}
	}
	raw, _ := json.Marshal(auths)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "token") || strings.Contains(string(raw), "signature") {
		t.Fatal("credential details leaked")
	}
}
