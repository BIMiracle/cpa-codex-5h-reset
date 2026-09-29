# CPA Codex Window Keeper

CLIProxyAPI (CPA) 的 Windows 原生插件。它通过 CPA 内部的 `host.auth.list` 找到所有启用的 Codex 凭据，使用 `host.model.execute` 的 `auth_id` 对每个凭据直接发起一次轻量请求。无需额外的下游唤醒 Key，也不会经过 `key-account-bind` 的调度选择。

## 行为

- 每天按 `Asia/Singapore` 的 05:00、10:01、15:02、20:03 为**每个**已启用的 Codex 凭据建立独立任务。两个账号同时排队、分别执行；一个账号失败不会阻塞另一个。
- 请求使用 `gpt-6-luna`、`reasoning_effort: low`（CPA 中的 light/低推理档）和短提示。
- HTTP 500、429、408、409、连接错误等临时错误，由插件在 CPA 进程中按退避间隔自动重试。每次请求都继续绑定原来的 `auth_id`。
- 成功响应有 `X-Codex-Primary-Reset-At` 或 `X-Codex-Primary-Reset-After-Seconds` 时，插件根据**该凭据的**实际重置时间决定是否等待并再次唤醒。若旧窗口到达时间超过本次任务的 `max_delay_minutes`，状态为 `already_active`，下个计划时点会再检查。
- 没有重置 Header 时只报告 `request_ok_reset_unknown`，不宣称窗口已重置。`fresh_window` 也是依据重置时间接近五小时作出的判断，不能证明上游确实重新分配了额度。
- 每个任务的状态写入独立 JSON 文件。CPA 重启后，在任务截止时间内继续重试；超过截止时间的旧任务不会补发。电脑关机或 CPA 未运行时，插件无法在指定时刻发请求。
- 最终失败可从 CPA 交互式进程执行 Windows 弹窗脚本。若 CPA 在非交互会话中运行，弹窗可能不可见；状态接口仍会留下失败记录。
- 只注册管理 API，不注册 `scheduler.pick`，可与 `key-account-bind` 同时使用。此插件不负责普通请求的额度均衡和会话粘性。

## 上传到 GitHub 并生成 DLL

把本目录所有内容（含隐藏的 `.github`）上传到你自己的 GitHub 仓库根目录。然后创建并推送标签 `v0.1.0`：

```powershell
git init
git add .
git commit -m "Initial CPA Codex Window Keeper"
git branch -M main
git remote add origin https://github.com/YOUR_NAME/cpa-codex-window-keeper.git
git push -u origin main
git tag v0.1.0
git push origin v0.1.0
```

GitHub Actions 会运行测试，再在 Windows runner 上编译 C ABI DLL，并在 Releases 中生成：

- `cpa-codex-window-keeper_0.1.0_windows_amd64.zip`
- `checksums.txt`

将这两个文件下载到本机。仓库 `go.mod` 中的示例 module 路径只用于本地编译，不影响 DLL 的插件 ID；如要让别人 `go get`，再改为真实仓库路径并同步改动两个源码 import。

插件先按下文手动安装即可。上传你自己的 GitHub 仓库不会自动让它出现在 CPA 官方插件商店；如需商店安装，还要在 release 可下载后向 [CLIProxyAPI-Plugins-Store](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store) 提交注册表条目。

## 安装到这台 Windows 11 的 CPA

当前 CPA 位于 `C:\software\CLIProxyAPI`，已有 `key-account-bind`。以下安装脚本会校验 ZIP SHA256，把新 DLL 与现有 DLL 放到 `C:\Users\bimir\AppData\Local\CLIProxyAPI\plugins\windows\amd64`，并准备弹窗脚本。请用**运行 CPA 的同一个 Windows 用户**执行安装脚本，确保该用户能写入状态文件：

```powershell
& ".\scripts\install-windows.ps1" `
  -ReleaseZip "C:\Downloads\cpa-codex-window-keeper_0.1.0_windows_amd64.zip" `
  -ChecksumsFile "C:\Downloads\checksums.txt"
```

请按实际下载位置改路径。在 `C:\software\CLIProxyAPI\config.yaml` 中把现有 `plugins.dir` 改为绝对路径，并在现有 `plugins.configs` 下增加：

```yaml
plugins:
  enabled: true
  dir: "C:\\Users\\bimir\\AppData\\Local\\CLIProxyAPI\\plugins"
  configs:
    cpa-codex-window-keeper:
      enabled: true
      model: gpt-6-luna
      reasoning_effort: low
      prompt: "Reply with OK."
      timezone: Asia/Singapore
      times: ["05:00", "10:01", "15:02", "20:03"]
      state_file: "C:\\Users\\bimir\\AppData\\Local\\CLIProxyAPI\\state\\window-keeper.json"
      retry_attempts: 50
      retry_initial_seconds: 15
      retry_max_seconds: 180
      max_delay_minutes: 180
      grace_seconds: 5
      notification_script: "C:\\Users\\bimir\\AppData\\Local\\CLIProxyAPI\\scripts\\notify-failure.ps1"
```

上面是需要**并入现有 YAML** 的示例，尤其要保留原来的 `key-account-bind` 配置，不能用此片段覆盖整个 `plugins:` 块。改完后重启 CPA，确认管理界面中的两个插件都已加载。`plugins.dir`、状态文件和弹窗脚本位于 CPA 程序目录之外；以后升级 CPA 时，保留 `config.yaml` 中的这段设置，就能继续加载插件。若全新安装 CPA 或重建配置文件，需要重新加入该配置。CPA 的原生插件 ABI 若在未来主版本变更，仍需重新编译和验证，无法无条件保证兼容。

确认插件状态接口可用并手动唤醒两个账号后，关闭旧的两个 Windows 计划任务，避免重复唤醒：

```powershell
Disable-ScheduledTask -TaskName "CPA Codex Wake A"
Disable-ScheduledTask -TaskName "CPA Codex Wake B"
```

**在完成验证前保留这两个旧任务。**

这台电脑当前的 `CLI Proxy API - Current` 计划任务只在**用户登录时**启动 CPA。电脑保持登录但处于睡眠状态时，可选地运行 `.\scripts\setup-computer-wake.ps1`，创建四个仅唤醒电脑的计划触发器；它们**不会发起模型请求或处理重试**，重试仍只在 CPA 插件中进行。如果 05:00 之前未登录、CPA 未启动、电脑已关机，单靠此插件和唤醒触发器均不能保证 05:00 执行。此时需把 CPA 配成可在登录前运行的服务/任务，并接受后台会话可能无法显示弹窗。

## 查看状态和手动唤醒

以下接口属于 CPA 管理 API，需要 CPA 管理密钥；不要把密钥写进仓库。`POST /run` 会立即排队，返回 HTTP 202；随后查看 `/status` 确认最终结果。空 JSON 对全部启用的 Codex 凭据运行；指定 `auth_id` 只运行一个。

```powershell
$managementKey = Read-Host "CPA management key"
$headers = @{ Authorization = "Bearer $managementKey" }
$base = "http://localhost:8317/v0/management/plugins/cpa-codex-window-keeper"

Invoke-RestMethod -Uri "$base/status" -Headers $headers
Invoke-RestMethod -Uri "$base/run" -Method Post -Headers $headers `
  -ContentType "application/json" -Body "{}"
# 仅运行一个账号：-Body '{"auth_id":"从 /status 取得的凭据 ID"}'
```

状态中的 `retry_pending` 表示插件将在 `next_attempt` 再请求；`wait_for_reset` 表示上游报告旧窗口尚未结束；`fresh_window` 表示请求成功且重置时间接近五小时后；`failed` 表示重试上限或截止时间已到。若 `http_500` 最后仍失败，可再次使用 `POST /run` 手动开始新任务。

## 原理和限制

固定时间之间仅相隔 5 小时 1 分钟。Codex 的额度窗口可能被你正在运行的 Codex 请求提前开始或延后重置；插件不能强制重置上游额度。它会按每个账号的响应 Header 调整自己的后续请求，尽量保持两个账号都被唤醒。CPA 的客户端 Header 透传选项无需开启，因为插件内部调用会拿到经过筛选的上游 Header。

任何唤醒请求本身也会消耗少量额度。普通使用继续通过你现有的一个日常 Key 及 `key-account-bind`/CPA 路由设置进行；两个专用唤醒 Key 可以在切换并验证完成后自行撤销。

源码许可：MIT。未直接复用第三方配额唤醒插件代码。
