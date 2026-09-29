package main

import (
	"encoding/json"
	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostProtocolAndQuota(t *testing.T) {
	old := invokeHost
	defer func() { invokeHost = old }()
	calls := []string{}
	invokeHost = func(method string, input any) (json.RawMessage, error) {
		calls = append(calls, method)
		m := input.(map[string]any)
		switch method {
		case "host.auth.get":
			if m["auth_index"] != "index-a" {
				t.Fatal("wrong credential")
			}
			return json.RawMessage(`{"json":{"access_token":"secret-token","account_id":"account-a"}}`), nil
		case "host.http.do":
			if m["URL"] != "https://chatgpt.com/backend-api/wham/usage" || m["Headers"].(http.Header).Get("Authorization") != "Bearer secret-token" {
				t.Fatal("quota transport")
			}
			body := []byte(`{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"reset_at":1790665500,"used_percent":42}}}`)
			raw, _ := json.Marshal(map[string]any{"StatusCode": 200, "Body": body})
			return raw, nil
		case "host.model.execute":
			if m["auth_id"] != "a" || m["forced_provider"] != "codex" {
				t.Fatal("unbound execution")
			}
			return json.RawMessage(`{"status_code":200}`), nil
		}
		t.Fatal(method)
		return nil, nil
	}
	h := host{}
	q, err := h.FetchQuota(keeper.Auth{ID: "a", AuthIndex: "index-a"}, time.Now())
	if err != nil || !q.Known || q.UsedPercent != 42 {
		t.Fatalf("%+v %v", q, err)
	}
	raw, _ := json.Marshal(q)
	if strings.Contains(string(raw), "secret-token") {
		t.Fatal("secret leak")
	}
	if _, err = h.Execute(keeper.Auth{ID: "a"}, keeper.Defaults()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatal(calls)
	}
}
func TestManagementPaths(t *testing.T) {
	old := engine
	engine = keeper.New(host{})
	defer func() { engine.Stop(); engine = old }()
	c := keeper.Defaults()
	c.StateFile = filepath.Join(t.TempDir(), "state.json")
	if err := engine.Configure(c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/v0/management/plugins/" + pluginID + "/status", 200}, {"GET", "/v0/management/plugins/" + pluginID + "/logs", 200}, {"GET", "/v0/resource/plugins/" + pluginID + "/ui", 200}, {"GET", "/v0/resource/plugins/" + pluginID + "/app.js", 200}, {"GET", "/evil/status", 404}, {"POST", "/v0/management/plugins/" + pluginID + "/run", 400}} {
		raw, err := handleManagement(managementRequest{Method: tc.method, Path: tc.path, Body: []byte(`bad`)})
		if err != nil {
			t.Fatal(err)
		}
		var env envelope
		_ = json.Unmarshal(raw, &env)
		var res managementResponse
		_ = json.Unmarshal(env.Result, &res)
		if res.StatusCode != tc.status {
			t.Fatalf("%s: %s", tc.path, raw)
		}
		if strings.Contains(tc.path, "resource") && (strings.Contains(string(res.Body), c.StateFile) || strings.Contains(string(res.Body), "Bearer secret")) {
			t.Fatal("resource leaks state")
		}
	}
}
