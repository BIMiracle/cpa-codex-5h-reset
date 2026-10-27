package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
	"net/http"
	"os"
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

func TestHolidayManagementPreviewImportAndValidation(t *testing.T) {
	previous, previousHost := engine, invokeHost
	engine = keeper.New(host{}, func(context.Context, string) ([]byte, error) { return nil, errors.New("offline fixture") })
	defer func() { engine.Stop(); engine, invokeHost = previous, previousHost }()
	invokeHost = func(method string, _ any) (json.RawMessage, error) {
		t.Errorf("holiday endpoints must not call account host methods: %s", method)
		return nil, errors.New("unexpected host callback")
	}
	c := keeper.Defaults()
	c.StateFile = filepath.Join(t.TempDir(), "state.json")
	if err := engine.Configure(c); err != nil {
		t.Fatal(err)
	}
	call := func(method, suffix, body string) managementResponse {
		t.Helper()
		raw, err := handleManagement(managementRequest{Method: method, Path: "/v0/management/plugins/" + pluginID + suffix, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		var env envelope
		var response managementResponse
		if json.Unmarshal(raw, &env) != nil || json.Unmarshal(env.Result, &response) != nil {
			t.Fatal("invalid response")
		}
		return response
	}
	data := `{"year":2026,"days":[{"date":"2026-10-01","name":"国庆","isOffDay":true}]}`
	preview := call("POST", "/holidays/import", `{"preview":true,"data":`+data+`}`)
	if preview.StatusCode != 200 || !strings.Contains(string(preview.Body), `"off_days":1`) {
		t.Fatalf("%s", preview.Body)
	}
	if _, err := os.Stat(c.StateFile + ".holidays.json"); !os.IsNotExist(err) {
		t.Fatal("preview wrote data")
	}
	if result := call("POST", "/holidays/import", `{"preview":false,"data":`+data+`}`); result.StatusCode != 200 {
		t.Fatalf("%s", result.Body)
	}
	if result := call("GET", "/holidays", ""); result.StatusCode != 200 || !strings.Contains(string(result.Body), `"manual":true`) {
		t.Fatalf("%s", result.Body)
	}
	if result := call("POST", "/holidays/refresh", ""); result.StatusCode != 202 {
		t.Fatal(result.StatusCode)
	}
	if result := call("POST", "/holidays/clear-import", `{"year":2026}`); result.StatusCode != 200 || strings.Contains(string(result.Body), `"manual":true`) {
		t.Fatalf("%s", result.Body)
	}
	for _, body := range []string{`bad`, `{}`, `{"data":{}}`, `{"data":{"year":2026,"days":[]}}`, `{"preview":"yes","data":{}}`} {
		if result := call("POST", "/holidays/import", body); result.StatusCode != 400 {
			t.Fatal("invalid import accepted", body)
		}
	}
	if result := call("POST", "/holidays/clear-import", `{"year":0}`); result.StatusCode != 400 {
		t.Fatal("invalid year accepted")
	}
	for _, timezone := range []string{"America/Los_Angeles", "not-a-timezone"} {
		raw, err := handleManagement(managementRequest{Method: "GET", Path: "/v0/management/plugins/" + pluginID + "/holidays", Query: map[string][]string{"timezone": {timezone}}})
		if err != nil {
			t.Fatal(err)
		}
		var env envelope
		var response managementResponse
		_ = json.Unmarshal(raw, &env)
		_ = json.Unmarshal(env.Result, &response)
		if timezone == "not-a-timezone" && response.StatusCode != 400 {
			t.Fatal("invalid timezone accepted")
		}
		if timezone != "not-a-timezone" && (response.StatusCode != 200 || !strings.Contains(string(response.Body), timezone)) {
			t.Fatal("timezone preview failed")
		}
	}
	raw, err := handleMethod("management.register", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/holidays\"", "/holidays/refresh", "/holidays/import", "/holidays/clear-import"} {
		if !strings.Contains(string(raw), suffix) {
			t.Fatal("route not registered", suffix)
		}
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

func TestQuotaQueryDoesNotWakeAccounts(t *testing.T) {
	old := invokeHost
	defer func() { invokeHost = old }()
	calls := []string{}
	invokeHost = func(method string, input any) (json.RawMessage, error) {
		calls = append(calls, method)
		switch method {
		case "host.auth.list":
			return json.RawMessage(`{"files":[{"id":"a","auth_index":"index-a","provider":"codex"},{"id":"disabled","provider":"codex","disabled":true},{"id":"other","provider":"claude"},{"id":"missing","provider":"codex"}]}`), nil
		case "host.auth.get":
			return json.RawMessage(`{"json":{"access_token":"secret-token","plan_type":"plus"}}`), nil
		case "host.http.do":
			body := []byte(`{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"reset_at":1790665500,"used_percent":42}}}`)
			raw, _ := json.Marshal(map[string]any{"StatusCode": 200, "Body": body})
			return raw, nil
		default:
			t.Fatalf("quota query must not invoke %s", method)
			return nil, nil
		}
	}
	raw, err := handleManagement(managementRequest{Method: "GET", Path: "/v0/management/plugins/" + pluginID + "/account-quotas"})
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.Unmarshal(raw, &env)
	var res managementResponse
	_ = json.Unmarshal(env.Result, &res)
	var data struct {
		Auths  []keeper.Auth           `json:"auths"`
		Quotas map[string]keeper.Quota `json:"quotas"`
	}
	if err = json.Unmarshal(res.Body, &data); err != nil || res.StatusCode != 200 {
		t.Fatalf("quota response: %s, %v", res.Body, err)
	}
	if len(data.Auths) != 3 || !data.Quotas["a"].Known || data.Quotas["a"].UsedPercent != 42 || data.Quotas["missing"].Known {
		t.Fatalf("unexpected quotas: %+v", data)
	}
	if len(calls) != 4 || data.Auths[0].PlanType != "plus" || strings.Contains(string(res.Body), "secret-token") {
		t.Fatalf("unexpected callbacks or secret leak: %v", calls)
	}
}
