package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
    uint32_t abi_version;
    void* host_ctx;
    cliproxy_host_call_fn call;
    cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;
extern int keeperPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void keeperPluginFree(void*, size_t);
extern void keeperPluginShutdown(void);
static const cliproxy_host_api* stored_host;
static void store_host_api(const cliproxy_host_api* host) { stored_host = host; }
static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
    if (stored_host == NULL || stored_host->call == NULL) return 1;
    return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}
static void free_host_buffer(void* ptr, size_t len) {
    if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL)
        stored_host->free_buffer(ptr, len);
}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unsafe"

	"github.com/example/cpa-codex-window-keeper/internal/keeper"
	"gopkg.in/yaml.v3"
)

const pluginID = "cpa-codex-window-keeper"

var engine = keeper.New(host{})

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}
type callbackError struct {
	status int
}

func (e callbackError) Error() string   { return "host callback failed" }
func (e callbackError) StatusCode() int { return e.status }

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(api *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if api == nil || plugin == nil || api.abi_version != 1 {
		return 1
	}
	C.store_host_api(api)
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.keeperPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.keeperPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.keeperPluginShutdown)
	return 0
}

//export keeperPluginCall
func keeperPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil || method == nil {
		return 1
	}
	response.ptr, response.len = nil, 0
	var raw []byte
	if request != nil && requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	answer, err := handleMethod(C.GoString(method), raw)
	if err != nil {
		answer = errorEnvelope("plugin_error", err.Error(), 0)
	}
	if len(answer) > 0 {
		response.ptr = C.CBytes(answer)
		response.len = C.size_t(len(answer))
	}
	if err != nil {
		return 1
	}
	return 0
}

//export keeperPluginFree
func keeperPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export keeperPluginShutdown
func keeperPluginShutdown() { engine.Stop() }

func okEnvelope(value any) ([]byte, error) {
	result, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}
func errorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(envelope{Error: &rpcError{Code: code, Message: message, HTTPStatus: status}})
	return raw
}

func callHost(method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cmethod := C.CString(method)
	defer C.free(unsafe.Pointer(cmethod))
	ptr := C.CBytes(body)
	if ptr == nil {
		return nil, errors.New("host request allocation failed")
	}
	defer C.free(ptr)
	var response C.cliproxy_buffer
	code := C.call_host_api(cmethod, (*C.uint8_t)(ptr), C.size_t(len(body)), &response)
	var raw []byte
	if response.ptr != nil {
		raw = C.GoBytes(response.ptr, C.int(response.len))
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(raw) == 0 {
		return nil, callbackError{}
	}
	var reply envelope
	if json.Unmarshal(raw, &reply) != nil {
		return nil, errors.New("invalid host response")
	}
	if !reply.OK {
		status := 0
		if reply.Error != nil {
			status = reply.Error.HTTPStatus
		}
		return nil, callbackError{status: status}
	}
	if code != 0 {
		return nil, callbackError{}
	}
	return reply.Result, nil
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}
type managementRequest struct {
	Method string `json:"Method"`
	Path   string `json:"Path"`
	Body   []byte `json:"Body"`
}
type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		var req lifecycleRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		cfg := keeper.Defaults()
		if len(req.ConfigYAML) > 0 {
			decoder := yaml.NewDecoder(strings.NewReader(string(req.ConfigYAML)))
			if err := decoder.Decode(&cfg); err != nil {
				return nil, fmt.Errorf("plugin config: %w", err)
			}
		}
		if err := engine.Configure(cfg); err != nil {
			return nil, err
		}
		return okEnvelope(map[string]any{
			"schema_version": 6,
			"metadata": map[string]any{
				"Name": "Codex Window Keeper", "Version": "0.1.0", "Author": "Community",
				"ConfigFields": []map[string]any{
					{"Name": "model", "Type": "string", "Description": "Codex model for wake requests"},
					{"Name": "reasoning_effort", "Type": "string", "Description": "Reasoning effort; low is the light setting"},
					{"Name": "timezone", "Type": "string", "Description": "IANA timezone"},
					{"Name": "times", "Type": "array", "Description": "Daily target times in HH:MM"},
				},
			},
			"capabilities": map[string]any{"management_api": true},
		})
	case "plugin.quiesce", "plugin.shutdown":
		engine.Stop()
		return okEnvelope(map[string]any{})
	case "management.register":
		return okEnvelope(map[string]any{"routes": []map[string]string{
			{"Method": "GET", "Path": "/plugins/" + pluginID + "/status"},
			{"Method": "POST", "Path": "/plugins/" + pluginID + "/run"},
		}})
	case "management.handle":
		var req managementRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		switch {
		case req.Method == http.MethodGet && strings.HasSuffix(req.Path, "/status"):
			return jsonManagement(http.StatusOK, engine.Status(time.Now()))
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, "/run"):
			var input struct {
				AuthID string `json:"auth_id"`
			}
			if len(req.Body) > 0 {
				if err := json.Unmarshal(req.Body, &input); err != nil {
					return jsonManagement(http.StatusBadRequest, map[string]string{"error": "invalid_json"})
				}
			}
			keys, err := engine.Manual(input.AuthID, time.Now())
			if err != nil {
				return jsonManagement(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			return jsonManagement(http.StatusAccepted, map[string]any{"queued": keys})
		default:
			return jsonManagement(http.StatusNotFound, map[string]string{"error": "route_not_found"})
		}
	default:
		return errorEnvelope("unknown_method", "unsupported plugin method", 0), nil
	}
}

func jsonManagement(status int, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{StatusCode: status,
		Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Body: body})
}
