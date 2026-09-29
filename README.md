# CPA Codex 5h Reset · Codex 五小时重置唤醒

CLIProxyAPI（CPA）原生动态库插件，支持 Windows、macOS 和 Linux。按每日计划为指定的 Codex 账号发送轻量请求；失败后查询该账号的**五小时限额重置时间**，等待重置后再尝试，并提供管理页面、手动唤醒和失败提醒。

> 插件不能强制重置上游额度。唤醒请求本身会消耗少量额度；“请求成功”与“额度已重置”是不同状态。

## 功能与重试规则

- 默认处理所有启用的 Codex 凭据，也可选择账号。通过 `host.model.execute` 指定 `auth_id`，无需额外的下游唤醒 Key；不同账号独立执行，同一账号只有一个未完成任务。
- 默认模型 `gpt-6-luna`、推理档 `low`；可在页面更改。模型必须是当前 CPA 与该账号实际支持的名称，插件不会替换为其他模型。
- 默认时区 `Asia/Singapore`，每日 `05:00、10:01、15:02、20:03`。支持增删时点、逐项启停；全部停用后仍能手动唤醒。
- 失败后通过 CPA 读取对应凭据，并通过 `host.http.do` 请求官方 Codex usage 接口，只匹配 **18000 秒**窗口，不将周限额当作五小时限额。
- 已知未来重置时间：等待到重置时刻；重置时间未知或已过去：失败后等待 **30 秒**。每次重试失败重新查询，新的未来重置时间优先。
- `max_retries: 3` 表示首次请求之外最多再试三次，总计最多四次。等待、额度查询不扣次数；每次实际模型请求扣一次。可设为 `0`（首次失败即结束），最大 `120`。查询到新时间不重置计数。
- 成功响应仍处于旧窗口时，会等待重置后继续请求，后续请求也计入同一任务的有限重试预算。成功但无法确认重置时间时标记“请求成功 · 重置未知”。
- 未完成任务、计数、额度快照和有限日志持久化；重启后继续任务，已完成任务不重跑。新计划与同账号未完成任务合并；重复手动请求返回 HTTP 409。

**例子：**15:02 首次失败，查到 15:15 重置，则在 15:15、15:15:30、15:16 依次重试（假设没有新的未来重置时间）。三次全部失败后停止并提醒。调度精度约一秒，实际响应耗时和 CPA 内部请求重试会影响完成时间；插件重试次数指对 CPA 的调用次数。

## 安装

需要支持原生插件 ABI 1、schema 6、管理资源和 `host.model.execute` / `host.auth.get` / `host.http.do` 的 CPA。Windows 本机已使用 **CPA 8.0.4（d33f63f8）**加载验证。其他 CPA 版本应先验证兼容性。

### 通过 CPA 插件商店安装

仓库：[BIMiracle/cpa-codex-5h-reset](https://github.com/BIMiracle/cpa-codex-5h-reset)。发布 Release 后，在 `http://localhost:8317/management.html` 的插件商店中添加自定义源：

```text
https://raw.githubusercontent.com/BIMiracle/cpa-codex-5h-reset/main/registry.json
```

在该源中安装 **Codex 五小时重置唤醒**，启用插件，然后打开插件菜单同名页面。若管理页面没有添加源入口，可将下面字段并入 CPA 配置：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/BIMiracle/cpa-codex-5h-reset/main/registry.json
```

`registry.json` 和对应版本的 Release 必须先上传到同一仓库；只有源码还无法从商店下载安装。自定义源无需官方商店收录。

### 手动安装

从 Releases 下载与你的 **CPA 进程架构**相同的 ZIP 及 `checksums.txt`，核验 ZIP 的 SHA256。包内只有根目录动态库。

| 平台 | 架构 | 动态库 | 放置目录（相对于 `plugins.dir`） |
|---|---|---|---|
| Windows | amd64 | `cpa-codex-5h-reset.dll` | `windows/amd64/` |
| macOS | amd64 / arm64 | `cpa-codex-5h-reset.dylib` | `darwin/amd64/` 或 `darwin/arm64/` |
| Linux | amd64 / arm64 | `cpa-codex-5h-reset.so` | `linux/amd64/` 或 `linux/arm64/` |

Linux 构建使用 glibc，amd64 以 Ubuntu 22.04、arm64 以 Ubuntu 24.04 为基线；Alpine/musl 需在目标环境重新编译。Windows ARM、Linux ARMv7 暂无预编译包。macOS Gatekeeper 阻止加载时，请按组织安全政策验证来源，不要盲目关闭系统保护。

Windows 可使用附带脚本校验和安装：

```powershell
.\scripts\install-windows.ps1 `
  -ReleaseZip "C:\Downloads\cpa-codex-5h-reset_0.2.0_windows_amd64.zip" `
  -ChecksumsFile "C:\Downloads\checksums.txt"
```

默认稳定根目录为当前用户 `$env:LOCALAPPDATA\CLIProxyAPI`，可通过 `-StableRoot` 修改。改变已有 `plugins.dir` 时，可用 `-ExistingPluginsDir` 显式指定旧插件目录，脚本保留其他插件文件。使用**运行 CPA 的同一个用户**安装，保证状态目录可写。

macOS/Linux：用 `shasum -a 256` / `sha256sum` 校验 ZIP，解压后将动态库放入上表目录；复制和解压不需要运行第三方安装脚本。重启 CPA 或使用其插件重新加载功能。

## 配置与管理页面

将以下配置**合并到现有** `plugins` 块，保留 `key-account-bind` 等其他插件的配置。路径替换为本机稳定目录的绝对路径。

```yaml
plugins:
  enabled: true
  dir: /absolute/stable/CLIProxyAPI/plugins
  configs:
    cpa-codex-5h-reset:
      enabled: true
      priority: 1
      model: gpt-6-luna
      reasoning_effort: low
      prompt: "Reply with OK."
      timezone: Asia/Singapore
      max_retries: 3
      auth_ids: [] # 空列表 = 所有启用的 Codex 账号，包括以后新增的账号
      desktop_notifications: false
      max_log_entries: 200
      state_file: /absolute/stable/CLIProxyAPI/state/cpa-codex-5h-reset.json
      schedules:
        - {id: morning, at: "05:00", enabled: true}
        - {id: midday, at: "10:01", enabled: true}
        - {id: afternoon, at: "15:02", enabled: true}
        - {id: evening, at: "20:03", enabled: true}
```

Windows 推荐路径形如 `C:/Users/<用户名>/AppData/Local/CLIProxyAPI/plugins`；可在 PowerShell 用 `Join-Path $env:LOCALAPPDATA 'CLIProxyAPI\plugins'` 得到当前用户路径。未显式指定 `state_file` 时，使用 Go `os.UserConfigDir()` 下的 `CLIProxyAPI/state/cpa-codex-5h-reset.json`；容器请显式配置持久卷路径。

安装后从 `management.html` 的插件菜单打开页面，或直接访问：

```text
http://localhost:8317/v0/resource/plugins/cpa-codex-5h-reset/ui
```

输入 **CPA Management Key** 后连接；密钥仅保存在当前页面内存，不写入本地存储。页面可保存设置、全部或逐账号手动唤醒，并展示额度用量、重置时间、下次尝试与日志。保存时会重新读取并保留 CPA 管理的 `enabled`、`priority`、商店信息和其他字段。

- 修改模型后，后续尝试使用新设置；进行中的任务保留创建时的最大重试次数。
- 取消某个每日时点不取消已经排队的任务；禁用、删除或取消选择某凭据后，其未完成任务会被取消。
- 关闭插件会暂停任务；重新启用后继续。首次安装只从当前分钟开始调度，不补发之前错过的所有时点。
- 状态文件保留最近七天已完成任务和所有未完成任务；日志最多 `max_log_entries` 条。

## 失败提醒

中途失败只更新状态，**重试全部失败后**弹出页面对话框。点击“开启网页通知”并允许权限后，会同时通过浏览器向 macOS、Windows 或支持的 Linux 桌面发送系统通知。通知按任务 ID 去重，刷新页面不会反复提醒同一个失败任务。

网页通知需要页面保持打开、浏览器支持 Notifications API、系统允许提醒；通常需要 HTTPS 或 localhost。关闭页面后后台任务继续，重新连接时显示尚未通知的失败。管理密钥不保存在浏览器；浏览器本地存储仅保留已通知任务 ID。

如需关闭网页后仍提醒，可启用 `desktop_notifications`：Windows 使用桌面消息框，macOS 使用 `osascript` 通知，Linux 使用 `notify-send`。运行 CPA 的用户需要可访问桌面；服务、SSH、Docker 或未登录会话可能没有桌面。默认关闭；同时启用两种通知渠道可能分别提醒一次。

## 管理 API

以下接口均由 CPA 验证管理密钥，前缀为 `/v0/management/plugins/cpa-codex-5h-reset`：

| 方法与路径 | 功能 |
|---|---|
| `GET /status` | 当前配置、凭据列表、任务、额度快照、下一计划、状态文件错误 |
| `POST /run` | `{}` 唤醒全部所选账号；`{"auth_id":"..."}` 唤醒指定账号，返回 202 |
| `GET /logs` | 有界、脱敏的任务记录 |
| `GET /config`、`PUT /config` | CPA 标准配置接口，保存时保留非本插件拥有的字段 |

同账号已有任务时返回 409；“全部唤醒”若包含忙碌账号则整体返回 409，可改用逐账号按钮。HTML/JS 资源本身公开，但不携带管理数据。令牌、管理密钥、原始错误和请求正文不会写入插件的状态或日志。

## 从旧 Window Keeper 迁移

1. 备份原配置与状态文件，停止 `cpa-codex-window-keeper`，避免新旧插件同时运行。
2. 安装新动态库，将配置键改为 `cpa-codex-5h-reset`。旧 `times` 数组仍可读取；在新页面保存时转为 `schedules`。
3. 删除 `retry_attempts`、`retry_initial_seconds`、`retry_max_seconds`、`max_delay_minutes`、`grace_seconds`、`notification_script`；明确设置 `max_retries: 3`。旧的 50 次退避配置不会自动继承。
4. 新配置的 `state_file` 可先指向旧文件。首次加载会将 schema 1 转为 schema 2，保留已完成和未完成任务，保留已发生的请求次数，新上限为首次请求加三次重试。超过新预算的旧任务结束为失败。
5. 验证手动唤醒与状态后，再禁用旧的 Windows `CPA Codex Wake A/B` 任务，以免重复唤醒。现有 `key-account-bind` 可继续使用；插件不改变普通请求的轮询与会话粘性。旧专用唤醒 Key 可在切换验证后自行撤销。

更新 CPA 时保留稳定的插件目录、状态文件和 `config.yaml`。未来若原生插件 ABI 改变，需要重新构建验证，不能无条件保证兼容。电脑关机、睡眠或 CPA 未运行时无法准点发请求；插件不会代替操作系统启动或唤醒电脑。`scripts/setup-computer-wake.ps1` 仅为旧 Windows 环境的可选辅助脚本，不参与插件重试。

崩溃发生在请求已发出但结果未保存的瞬间时，无法知道上游是否已执行；恢复会保留已扣除的次数，在剩余预算内尝试。插件不承诺网络请求严格“恰好一次”。

## 构建与验证

安装 `go.mod` 指定的 Go 版本及目标平台 C 编译器：Windows 使用 MinGW-w64 GCC，macOS 使用 Xcode Command Line Tools，Linux 使用 GCC。

```sh
go test -race ./...
go vet ./...
node --test web/app.test.cjs
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -o cpa-codex-5h-reset.so .
python scripts/abi-smoke.py ./cpa-codex-5h-reset.so
```

Windows 在 PowerShell 设置 `$env:CGO_ENABLED='1'`、`$env:CC` 后构建 `.dll`；macOS 构建 `.dylib`。ABI 测试使用模拟凭据和回调，不访问真实账号。

GitHub Actions 为五个平台运行 Go 测试、竞态检查、静态检查、网页逻辑测试及动态库 ABI 冒烟测试。手动触发工作流可下载构建包；推送 `v0.2.0` 等标签会汇总 ZIP、生成 `checksums.txt` 并发布 Release。发布新版本时同步更新 `registry.json` 的版本。

单元测试覆盖实际重置等待、30 秒重试、零次和三次预算、账号独立执行、手动任务恢复、配置变化、凭据停用、额度未知和周限额过滤；网页测试覆盖保存字段保留、终态通知去重和拒绝权限降级。**构建/ABI 通过不等于所有操作系统桌面通知均已实测**。

## 故障排查

- **插件未显示**：检查 CPA 插件总开关、目录层级、进程架构和动态库依赖；插件 ID 与文件名需匹配。
- **额度未知**：检查凭据是否支持官方 Codex usage、网络与认证状态。缺少 `auth_index` 或凭据过期时降级为 30 秒有限重试，不虚构时间。
- **等待很久**：查看该账号真实重置时间；不会为了固定时点提前重置额度，也不再采用旧的三小时截止限制。
- **无法保存状态**：检查运行 CPA 用户的目录写权限、磁盘空间。状态无法落盘时不继续发送新请求。
- **401 / 通知未出现**：分别检查 Management Key、网页是否仍打开，以及浏览器和系统通知权限。

许可：MIT。参考 CPA 原生插件接入方式，未复制第三方插件实现代码。
