package main

import (
	"encoding/json"
	"errors"
	"github.com/BIMiracle/cpa-codex-5h-reset/internal/keeper"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
)

type host struct{}

// Injectable boundary for protocol tests. Production always uses the C host callback.
var invokeHost = callHost

func (host) ListAuths() ([]keeper.Auth, error) {
	raw, err := invokeHost("host.auth.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var result struct {
		Files []keeper.Auth `json:"files"`
	}
	err = json.Unmarshal(raw, &result)
	if err != nil {
		return nil, err
	}
	for i := range result.Files {
		a := &result.Files[i]
		a.PlanType = strings.ToLower(strings.TrimSpace(a.PlanType))
		if !strings.EqualFold(a.Provider, "codex") || a.Disabled || a.PlanType != "" || a.AuthIndex == "" {
			continue
		}
		// The CPA auth-list ABI omits plan metadata. Read the credential locally;
		// retain only the plan, never the token or any other credential fields.
		credential, err := invokeHost("host.auth.get", map[string]any{"auth_index": a.AuthIndex})
		if err == nil {
			a.PlanType = credentialPlan(credential)
		}
	}
	return result.Files, nil
}
func (host) Execute(a keeper.Auth, c keeper.Config) (keeper.Execution, error) {
	payload := map[string]any{"model": c.Model, "messages": []map[string]string{{"role": "user", "content": c.Prompt}}, "stream": false}
	if c.ReasoningEffort != "" {
		payload["reasoning_effort"] = c.ReasoningEffort
	}
	body, _ := json.Marshal(payload)
	raw, err := invokeHost("host.model.execute", map[string]any{"entry_protocol": "openai", "exit_protocol": "openai", "model": c.Model, "stream": false, "body": body, "headers": http.Header{"Content-Type": {"application/json"}}, "forced_provider": "codex", "auth_id": a.ID})
	if err != nil {
		var coded interface{ StatusCode() int }
		if errors.As(err, &coded) {
			return keeper.Execution{StatusCode: coded.StatusCode()}, err
		}
		return keeper.Execution{}, err
	}
	var result keeper.Execution
	err = json.Unmarshal(raw, &result)
	return result, err
}
func (host) Log(level, event string, fields map[string]any) {
	fields["plugin_id"] = pluginID
	_, _ = invokeHost("host.log", map[string]any{"level": level, "message": event, "fields": fields})
}
func (h host) Notify(_ string, _ string, c keeper.Config) {
	if !c.DesktopNotifications {
		return
	}
	var cmd *exec.Cmd
	// Static strings only; never interpolate credential names into shell source.
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", `Add-Type -AssemblyName PresentationFramework; [System.Windows.MessageBox]::Show('Codex five-hour wake failed after all retries. Open the CPA plugin page for details.','CPA Codex 5h Reset') | Out-Null`)
	case "darwin":
		cmd = exec.Command("osascript", "-e", `display notification "Wake failed after all retries. Open CPA for details." with title "CPA Codex 5h Reset"`)
	case "linux":
		cmd = exec.Command("notify-send", "CPA Codex 5h Reset", "Wake failed after all retries. Open CPA for details.")
	default:
		return
	}
	if err := cmd.Start(); err != nil {
		h.Log("warn", "desktop_notification_unavailable", map[string]any{})
		return
	}
	go func() { _ = cmd.Wait() }()
}
