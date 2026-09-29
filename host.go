package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"runtime"

	"github.com/example/cpa-codex-window-keeper/internal/keeper"
)

type host struct{}

func (host) ListAuths() ([]keeper.Auth, error) {
	raw, err := callHost("host.auth.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var result struct {
		Files []keeper.Auth `json:"files"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result.Files, nil
}

func (host) Execute(auth keeper.Auth, cfg keeper.Config) (keeper.Execution, error) {
	body, _ := json.Marshal(map[string]any{
		"model": cfg.Model, "reasoning_effort": cfg.ReasoningEffort,
		"messages": []map[string]string{{"role": "user", "content": cfg.Prompt}},
		"stream":   false,
	})
	raw, err := callHost("host.model.execute", map[string]any{
		"entry_protocol": "openai", "exit_protocol": "openai", "model": cfg.Model,
		"stream": false, "body": body,
		"headers":         http.Header{"Content-Type": []string{"application/json"}},
		"forced_provider": "codex", "auth_id": auth.ID,
	})
	if err != nil {
		var coded interface{ StatusCode() int }
		if errors.As(err, &coded) {
			return keeper.Execution{StatusCode: coded.StatusCode()}, err
		}
		return keeper.Execution{}, err
	}
	var result keeper.Execution
	if err := json.Unmarshal(raw, &result); err != nil {
		return keeper.Execution{}, err
	}
	return result, nil
}

func (host) Log(level, event string, fields map[string]any) {
	fields["plugin_id"] = pluginID
	_, _ = callHost("host.log", map[string]any{
		"level": level, "message": event, "fields": fields,
	})
}

func (h host) Notify(authID, reason string, cfg keeper.Config) {
	if cfg.NotificationScript == "" || runtime.GOOS != "windows" {
		return
	}
	command := exec.Command("powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass",
		"-File", cfg.NotificationScript, "-Auth", authID, "-Reason", reason)
	if err := command.Start(); err != nil {
		h.Log("error", "notification_failed", map[string]any{"error_type": "process_start"})
		return
	}
	go func() { _ = command.Wait() }()
}
