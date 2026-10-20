package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
	"github.com/BIMiracle/cpa-codex-5h-reset/internal/routing"
)

func TestSchedulerRegistrationWireProtocolAndStatus(t *testing.T) {
	oldEngine, oldRouter, oldHost := engine, accountRouter, invokeHost
	engine, accountRouter = keeper.New(host{}), routing.New(host{})
	defer func() { engine.Stop(); engine, accountRouter, invokeHost = oldEngine, oldRouter, oldHost }()
	invokeHost = func(method string, input any) (json.RawMessage, error) {
		switch method {
		case "host.auth.list":
			return json.RawMessage(`{"files":[{"id":"a","auth_index":"a","provider":"codex"},{"id":"b","auth_index":"b","provider":"codex"}]}`), nil
		case "host.auth.get":
			id := input.(map[string]any)["auth_index"]
			return json.Marshal(map[string]any{"json": map[string]any{"access_token": "secret-" + id.(string)}})
		case "host.http.do":
			used := 76
			if input.(map[string]any)["Headers"].(http.Header).Get("Authorization") == "Bearer secret-b" {
				used = 21
			}
			body := []byte(fmt.Sprintf(`{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":%d,"reset_at":%d}}}`, used, time.Now().Add(time.Hour).Unix()))
			return json.Marshal(map[string]any{"StatusCode": 200, "Body": body})
		case "host.log":
			return json.RawMessage(`{}`), nil
		}
		t.Fatalf("unexpected callback: %s", method)
		return nil, nil
	}
	config, _ := json.Marshal(map[string]any{"enabled": false, "state_file": filepath.Join(t.TempDir(), "wake.json"), "quota_routing": map[string]any{"enabled": true}})
	payload, _ := json.Marshal(lifecycleRequest{ConfigYAML: config})
	raw, err := handleMethod("plugin.register", payload)
	if err != nil || !strings.Contains(string(raw), `"scheduler":true`) || !strings.Contains(string(raw), `"scheduler_across_priorities":true`) {
		t.Fatalf("scheduler capability missing: %s %v", raw, err)
	}
	raw, err = handleMethod("scheduler.pick", []byte(`{"Providers":["codex"],"Model":"gpt-6.1-sol","Options":{"Headers":{"Authorization":["Bearer client"],"Session_id":["new"]}},"Candidates":[{"ID":"a","Provider":"codex"},{"ID":"b","Provider":"codex"}]}`))
	if err != nil || !strings.Contains(string(raw), `"AuthID":"b"`) {
		t.Fatalf("wrong native ABI selection: %s %v", raw, err)
	}
	raw, err = handleMethod("scheduler.pick", []byte(`{"Options":{"Metadata":{"pinned_auth_id":"a"}},"Candidates":[{"ID":"a","Provider":"codex"}]}`))
	if err != nil || !strings.Contains(string(raw), `"AuthID":"a"`) {
		t.Fatalf("wake pin overridden: %s %v", raw, err)
	}
	raw, err = handleManagement(managementRequest{Method: "GET", Path: "/v0/management/plugins/" + pluginID + "/status"})
	var env envelope
	var result managementResponse
	if err != nil || json.Unmarshal(raw, &env) != nil || json.Unmarshal(env.Result, &result) != nil || !strings.Contains(string(result.Body), `"quota_routing":{"active_bindings":1`) || strings.Contains(string(result.Body), "secret-") {
		t.Fatalf("routing status absent or unsafe: %s %v", raw, err)
	}
}
