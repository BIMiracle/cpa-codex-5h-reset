package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
	"gopkg.in/yaml.v3"
	"net/http"
	"strings"
	"time"
)

var pluginVersion = "0.2.0"

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
		if err := engine.Configure(c); err != nil {
			return nil, err
		}
		return okEnvelope(map[string]any{"schema_version": 6, "metadata": map[string]any{"Name": "Codex 五小时重置唤醒", "Version": pluginVersion, "Author": "BIMiracle", "GitHubRepository": "https://github.com/BIMiracle/cpa-codex-5h-reset", "ConfigFields": []map[string]any{{"Name": "model", "Type": "string", "Description": "唤醒模型"}, {"Name": "max_retries", "Type": "number", "Description": "首次请求之外的重试次数，默认 3"}, {"Name": "timezone", "Type": "string", "Description": "IANA 时区"}}}, "capabilities": map[string]any{"management_api": true}})
	case "plugin.quiesce", "plugin.shutdown":
		engine.Stop()
		return okEnvelope(map[string]any{})
	case "management.register":
		return okEnvelope(map[string]any{"routes": []map[string]string{{"Method": "GET", "Path": "/plugins/" + pluginID + "/status"}, {"Method": "POST", "Path": "/plugins/" + pluginID + "/run"}, {"Method": "GET", "Path": "/plugins/" + pluginID + "/logs"}}, "resources": []map[string]string{{"Path": "/ui", "Menu": "Codex 五小时重置唤醒", "Description": "计划、五小时额度、手动唤醒和失败提醒"}, {"Path": "/app.js"}}})
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
		return jsonManagement(200, engine.Status(time.Now()))
	case req.Method == "GET" && path == api+"/logs":
		return jsonManagement(200, map[string]any{"logs": engine.Logs()})
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
