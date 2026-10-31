package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
	"github.com/BIMiracle/cpa-codex-5h-reset/internal/routing"
	"gopkg.in/yaml.v3"
	"net/http"
	"strings"
	"time"
)

var pluginVersion = "0.3.0"

//go:embed web/index.html
var page []byte

//go:embed web/app.js
var appJS []byte

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}
type managementRequest struct {
	Method string
	Path   string
	Body   []byte
	Query  map[string][]string
}
type managementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		var req lifecycleRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		c := keeper.Defaults()
		if len(req.ConfigYAML) > 0 {
			if err := yaml.Unmarshal(req.ConfigYAML, &c); err != nil {
				return nil, errors.New("invalid plugin configuration")
			}
		}
		if err := c.Validate(); err != nil {
			return nil, err
		}
		routingConfig, err := routing.Parse(req.ConfigYAML, c.StateFile)
		if err != nil {
			return nil, err
		}
		plan, err := accountRouter.Prepare(routingConfig)
		if err != nil {
			return nil, err
		}
		if err := engine.Configure(c); err != nil {
			return nil, err
		}
		accountRouter.Configure(plan)
		return okEnvelope(map[string]any{"schema_version": 6, "metadata": map[string]any{"Name": "Codex 五小时重置唤醒", "Version": pluginVersion, "Author": "BIMiracle", "GitHubRepository": "https://github.com/BIMiracle/cpa-codex-5h-reset", "ConfigFields": []map[string]any{{"Name": "model", "Type": "string", "Description": "唤醒模型"}, {"Name": "max_retries", "Type": "number", "Description": "首次请求之外的重试次数，默认 3"}, {"Name": "timezone", "Type": "string", "Description": "IANA 时区"}}}, "capabilities": map[string]any{"management_api": true, "scheduler": routingConfig.Enabled, "scheduler_across_priorities": routingConfig.Enabled}})
	case "scheduler.pick":
		var req routing.Request
		if json.Unmarshal(raw, &req) != nil {
			return errorEnvelope("invalid_request", "invalid scheduler request", 400), nil
		}
		decision := accountRouter.Pick(req)
		if decision.Reject {
			// A protocol error also fails closed on hosts predating Reject support.
			return errorEnvelope(decision.RejectCode, decision.RejectReason, 503), nil
		}
		return okEnvelope(decision)
	case "plugin.quiesce", "plugin.shutdown":
		engine.Stop()
		return okEnvelope(map[string]any{})
	case "management.register":
		return okEnvelope(map[string]any{"routes": []map[string]string{{"Method": "GET", "Path": "/plugins/" + pluginID + "/status"}, {"Method": "POST", "Path": "/plugins/" + pluginID + "/run"}, {"Method": "GET", "Path": "/plugins/" + pluginID + "/logs"}, {"Method": "GET", "Path": "/plugins/" + pluginID + "/account-quotas"}, {"Method": "GET", "Path": "/plugins/" + pluginID + "/holidays"}, {"Method": "POST", "Path": "/plugins/" + pluginID + "/holidays/refresh"}, {"Method": "POST", "Path": "/plugins/" + pluginID + "/holidays/import"}, {"Method": "POST", "Path": "/plugins/" + pluginID + "/holidays/clear-import"}}, "resources": []map[string]string{{"Path": "/ui", "Menu": "Codex 五小时重置唤醒", "Description": "计划、节假日、五小时额度、手动唤醒和失败提醒"}, {"Path": "/app.js"}}})
	case "management.handle":
		var req managementRequest
		if json.Unmarshal(raw, &req) != nil {
			return nil, errors.New("invalid management request")
		}
		return handleManagement(req)
	default:
		return errorEnvelope("unknown_method", "unsupported plugin method", 0), nil
	}
}
func handleManagement(req managementRequest) ([]byte, error) {
	path := strings.TrimPrefix(req.Path, "/v0/management")
	api := "/plugins/" + pluginID
	if req.Method == "GET" && (path == "/ui" || path == "/app.js" || path == "/v0/resource/plugins/"+pluginID+"/ui" || path == "/v0/resource/plugins/"+pluginID+"/app.js") {
		body, kind := page, "text/html; charset=utf-8"
		if strings.HasSuffix(path, "/app.js") {
			body, kind = appJS, "text/javascript; charset=utf-8"
		}
		return okEnvelope(managementResponse{200, http.Header{"Content-Type": {kind}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}, "Content-Security-Policy": {"default-src 'self'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; object-src 'none'; base-uri 'none'"}}, body})
	}
	switch {
	case req.Method == "GET" && path == api+"/status":
		status := engine.Status(time.Now())
		return jsonManagement(200, struct {
			keeper.Status
			Routing map[string]any `json:"quota_routing"`
		}{status, accountRouter.Status()})
	case req.Method == "GET" && path == api+"/logs":
		return jsonManagement(200, map[string]any{"logs": engine.Logs()})
	case req.Method == "GET" && path == api+"/holidays":
		timezone := ""
		if values := req.Query["timezone"]; len(values) > 0 {
			timezone = values[0]
			if _, err := time.LoadLocation(timezone); err != nil {
				return jsonManagement(400, map[string]string{"error": "invalid_timezone"})
			}
		}
		return jsonManagement(200, engine.HolidayCalendar(time.Now(), false, timezone))
	case req.Method == "POST" && path == api+"/holidays/refresh":
		return jsonManagement(202, engine.HolidayCalendar(time.Now(), true))
	case req.Method == "POST" && path == api+"/holidays/import":
		var input struct {
			Data    json.RawMessage `json:"data"`
			Preview bool            `json:"preview"`
		}
		if len(req.Body) > 3<<20 || json.Unmarshal(req.Body, &input) != nil || len(input.Data) == 0 {
			return jsonManagement(400, map[string]string{"error": "invalid_holiday_import"})
		}
		result, err := engine.ImportHolidays(input.Data, input.Preview, time.Now())
		if err != nil {
			code := 400
			if err.Error() == "holiday_write_failed" {
				code = 500
			}
			return jsonManagement(code, map[string]string{"error": err.Error()})
		}
		return jsonManagement(200, result)
	case req.Method == "POST" && path == api+"/holidays/clear-import":
		var input struct {
			Year int `json:"year"`
		}
		if json.Unmarshal(req.Body, &input) != nil || input.Year < 1900 || input.Year > 9998 {
			return jsonManagement(400, map[string]string{"error": "invalid_holiday_year"})
		}
		if err := engine.ClearImportedHolidays(input.Year); err != nil {
			return jsonManagement(500, map[string]string{"error": err.Error()})
		}
		return jsonManagement(200, engine.HolidayCalendar(time.Now(), true))
	case req.Method == "GET" && path == api+"/account-quotas":
		// Query usage without submitting a model request or creating a wake task.
		h := host{}
		auths, err := h.ListAuths()
		if err != nil {
			return jsonManagement(502, map[string]string{"error": "account_list_unavailable"})
		}
		accounts := []keeper.Auth{}
		quotas := map[string]keeper.Quota{}
		for _, a := range auths {
			if !strings.EqualFold(a.Provider, "codex") {
				continue
			}
			accounts = append(accounts, a)
			if a.Disabled {
				continue
			}
			now := time.Now()
			q, err := h.FetchQuota(a, now)
			if err != nil {
				if q.Error == "" {
					q = keeper.Quota{ObservedAt: now, Error: "quota_unavailable"}
				}
			}
			quotas[a.ID] = q
		}
		return jsonManagement(200, map[string]any{"auths": accounts, "quotas": quotas})
	case req.Method == "POST" && path == api+"/run":
		var input struct {
			AuthID string `json:"auth_id"`
		}
		if len(req.Body) > 0 && json.Unmarshal(req.Body, &input) != nil {
			return jsonManagement(400, map[string]string{"error": "invalid_json"})
		}
		keys, err := engine.Manual(input.AuthID, time.Now())
		if err != nil {
			status := 400
			if errors.Is(err, keeper.ErrConflict) {
				status = 409
			} else if errors.Is(err, keeper.ErrDisabled) {
				status = 409
			}
			return jsonManagement(status, map[string]string{"error": err.Error()})
		}
		return jsonManagement(202, map[string]any{"queued": keys})
	default:
		return jsonManagement(404, map[string]string{"error": "route_not_found"})
	}
}
func jsonManagement(status int, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{status, http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}}, body})
}
