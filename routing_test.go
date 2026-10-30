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
			return json.RawMessage(`{"files":[{"id":"a","auth_index":"a","provider":"codex","plan_type":"self_serve_business_prolite"},{"id":"b","auth_index":"b","provider":"codex","plan_type":"plus"},{"id":"c","auth_index":"c","provider":"codex","plan_type":"plus"}]}`), nil
		case "host.auth.get":
			id := input.(map[string]any)["auth_index"]
			return json.Marshal(map[string]any{"json": map[string]any{"access_token": "secret-" + id.(string)}})
		case "host.http.do":
			id := input.(map[string]any)["Headers"].(http.Header).Get("Authorization")
			weekly := map[string]int{"Bearer secret-a": 32, "Bearer secret-b": 2, "Bearer secret-c": 4}[id]
			windows := map[string]any{"primary_window": map[string]any{"limit_window_seconds": 604800, "used_percent": weekly, "reset_at": time.Now().Add(7 * 24 * time.Hour).Unix()}}
			if id != "Bearer secret-a" {
				used := map[string]int{"Bearer secret-b": 6, "Bearer secret-c": 24}[id]
				windows["secondary_window"] = map[string]any{"limit_window_seconds": 18000, "used_percent": used, "reset_at": time.Now().Add(time.Hour).Unix()}
			}
			body, _ := json.Marshal(map[string]any{"rate_limit": windows})
			return json.Marshal(map[string]any{"StatusCode": 200, "Body": body})
		case "host.log":
			return json.RawMessage(`{}`), nil
		}
		t.Fatalf("unexpected callback: %s", method)
		return nil, nil
	}
	config, _ := json.Marshal(map[string]any{"enabled": false, "state_file": filepath.Join(t.TempDir(), "wake.json"), "quota_routing": map[string]any{"enabled": true, "bindings": []map[string]any{{"key": "failover", "allow": []string{"c"}, "fallback_allow": []string{"a", "b"}}}}})
	payload, _ := json.Marshal(lifecycleRequest{ConfigYAML: config})
	raw, err := handleMethod("plugin.register", payload)
	if err != nil || !strings.Contains(string(raw), `"scheduler":true`) || !strings.Contains(string(raw), `"scheduler_across_priorities":true`) {
		t.Fatalf("scheduler capability missing: %s %v", raw, err)
	}
	for i := range 3 {
		request := fmt.Sprintf(`{"Providers":["codex"],"Model":"gpt-6.1-sol","Options":{"Headers":{"Authorization":["Bearer client"],"Session_id":["new-%d"]}},"Candidates":[{"ID":"a","Provider":"codex"},{"ID":"b","Provider":"codex"},{"ID":"c","Provider":"codex"}]}`, i)
		raw, err = handleMethod("scheduler.pick", []byte(request))
		if err != nil || !strings.Contains(string(raw), `"AuthID":"b"`) {
			t.Fatalf("wrong native ABI selection: %s %v", raw, err)
		}
	}
	raw, err = handleMethod("scheduler.pick", []byte(`{"Options":{"Metadata":{"pinned_auth_id":"a"}},"Candidates":[{"ID":"a","Provider":"codex"}]}`))
	if err != nil || !strings.Contains(string(raw), `"AuthID":"a"`) {
		t.Fatalf("wake pin overridden: %s %v", raw, err)
	}
	for i, want := range []string{"c", "b", "b"} {
		candidates := `[{"ID":"a","Provider":"codex"},{"ID":"b","Provider":"codex"},{"ID":"c","Provider":"codex"}]`
		if i == 1 {
			candidates = `[{"ID":"a","Provider":"codex"},{"ID":"b","Provider":"codex"}]`
		}
		request := fmt.Sprintf(`{"Model":"gpt-6.1-sol","Options":{"Headers":{"Authorization":["Bearer failover"],"Session_id":["ongoing"]}},"Candidates":%s}`, candidates)
		raw, err = handleMethod("scheduler.pick", []byte(request))
		if err != nil || !strings.Contains(string(raw), `"AuthID":"`+want+`"`) {
			t.Fatalf("native ABI fallback selection failed: %s %v", raw, err)
		}
	}
	raw, err = handleManagement(managementRequest{Method: "GET", Path: "/v0/management/plugins/" + pluginID + "/status"})
	var env envelope
	var result managementResponse
	if err != nil || json.Unmarshal(raw, &env) != nil || json.Unmarshal(env.Result, &result) != nil || !strings.Contains(string(result.Body), `"quota_routing":{"active_bindings":4`) || !strings.Contains(string(result.Body), `"strategy":"weekly-then-five-hour"`) || !strings.Contains(string(result.Body), `"weekly":`) || strings.Contains(string(result.Body), "secret-") {
		t.Fatalf("routing status absent or unsafe: %s %v", raw, err)
	}
}
