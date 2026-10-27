# CPA Codex 5h Reset

**Codex 五小时重置唤醒 · CLIProxyAPI（CPA）原生插件**

中文 | [English](README_EN.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/BIMiracle/cpa-codex-5h-reset)](https://github.com/BIMiracle/cpa-codex-5h-reset/releases)

为 CPA 中的 Codex Plus 账号定时发送轻量请求；失败后查询五小时额度重置时间，等待重置后重试。通过管理页面即可设置计划、查看额度和手动唤醒，支持 Windows、macOS 和 Linux。

还可作为 CPA 的账号调度器，按**周剩余额度优先、五小时剩余额度其次**分配请求，支持 Plus 与 Business Premium 混合使用、会话绑定及额度差距较大时重新分配，详见[额度调度规则](#周额度与五小时额度调度v030)。

五小时重置唤醒仅适用于 **Plus**。**Business Premium**、其他套餐及套餐未知的账号不参与定时、手动唤醒或重试；已有等待任务在检测到套餐不符合时取消。账号额度查询和额度路由仍独立运行。

> 插件不能强制重置上游额度。唤醒请求会消耗少量额度，请求成功不代表额度已重置；电脑与 CPA 需要保持运行。

## 主要功能

- **定时唤醒**：自定义时区和每日计划，支持全部或指定 Codex Plus 账号，无需额外的下游 API Key。
- **额度调度**：按周和五小时余量选择可用账号，余量接近时轮询；已有长会话也会根据最新额度有条件地切换账号。
- **日期安排**：时区下拉默认 `Asia/Singapore`；可跳过中国大陆放假日及普通周末，调休补班日照常运行。支持未来 30 天内一次性排除日期，或仅为明天覆盖休息日限制。
- **节假日日历**：周一至周日的六周月历，补齐相邻月份日期，可查看上个月、本月和下个月；多来源后台获取和本地缓存，支持年度 JSON 预览、手动导入与清除。所有日期规则仍允许手动唤醒。
- **有限重试**：失败后优先等待五小时额度重置；重置时间未知或已过时，等待 30 秒再试。默认最多重试 3 次。
- **可视化管理**：查看账号额度、重置时间、任务状态与日志，随时手动唤醒。
- **断点恢复与提醒**：任务状态持久化，重启后继续未完成任务；最终失败时支持网页与可选桌面通知。

## 安装与使用

### 1. 准备环境

需要已登录 Codex 账号的 CPA，并支持原生插件 **ABI 1 / schema 6**、管理资源及 `host.model.execute`、`host.auth.get`、`host.http.do`。已在 Windows CPA **8.0.4（d33f63f8）**验证加载，其他版本请先确认兼容性。

### 2. 安装插件

在 CPA [管理页面](http://localhost:8317/management.html)的插件商店添加自定义源：

```text
https://raw.githubusercontent.com/BIMiracle/cpa-codex-5h-reset/main/registry.json
```

安装并启用 **Codex 五小时重置唤醒**。若页面没有添加源入口，将以下内容合并到现有 CPA 配置的 `plugins` 块：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/BIMiracle/cpa-codex-5h-reset/main/registry.json
```

商店安装需要仓库已有对应版本的 Release。也可从 [Releases](https://github.com/BIMiracle/cpa-codex-5h-reset/releases) 下载匹配 **CPA 进程架构**的 ZIP，使用 `checksums.txt` 核验 SHA256 后，将动态库放入 `plugins.dir` 下对应目录，再重启或重新加载插件：

| 平台 | 架构 | 动态库 | 目录 |
| --- | --- | --- | --- |
| Windows | amd64 | `cpa-codex-5h-reset.dll` | `windows/amd64/` |
| macOS | amd64 / arm64 | `cpa-codex-5h-reset.dylib` | `darwin/<架构>/` |
| Linux | amd64 / arm64 | `cpa-codex-5h-reset.so` | `linux/<架构>/` |

Linux 预编译包依赖 glibc；Alpine/musl 需自行构建。手动安装的完整配置与 Windows 安装脚本用法见[详细指南](docs/guide.zh-CN.md#配置与管理页面)。

### 3. 设置并开始使用

1. 从 CPA 插件菜单打开同名页面，或访问[插件管理页面](http://localhost:8317/v0/resource/plugins/cpa-codex-5h-reset/ui)（远程部署请替换主机和端口）。
2. 页面自动复用管理中心当前登录会话，加载账号、五小时与周剩余额度及配置，无需再次输入管理密钥。
3. 选择账号、模型、时区与每日计划，保存后点击手动唤醒，确认任务状态。
4. 如需网页通知，点击“开启网页通知”并允许权限，保持页面打开。

页面不提供独立主题选项，实时跟随 CLI Proxy API 管理中心主题菜单的选择（纯白、羊毛纸、暗色或自动）。独立打开时读取同源管理中心的主题偏好和已记住的登录会话；会话不可用时请从已登录的管理中心插件菜单打开。额度在打开页面、手动刷新和每分钟自动查询时更新，查询不会触发唤醒请求。

若插件页面返回 **404**，先确认当前 CPA 进程已成功加载插件，而不只是配置中写了 `enabled: true`。检查启动时使用的配置文件、`plugins.dir` 与加载日志，再重新加载插件或重启 CPA；详见[故障排查](docs/guide.zh-CN.md#故障排查)。

| 设置 | 默认值 |
| --- | --- |
| 唤醒账号 | 所有启用的 Codex Plus 账号，包括以后新增的 Plus 账号 |
| 模型 / 推理档 | `gpt-6-luna` / `low`，须为当前 CPA 和账号支持的值 |
| 时区 | `Asia/Singapore` |
| 法定节假日 / 一次性日期规则 | 默认关闭 / 按默认规则 |
| 每日计划 | `05:00`、`10:01`、`15:02`、`20:03` |
| 最大重试次数 | `3`，即首次请求之外最多再试 3 次 |
| 桌面通知 | 默认关闭，可通过 `desktop_notifications` 开启 |
| 额度调度 | 默认关闭，通过 `quota_routing.enabled` 开启；账号池独立于唤醒账号选择 |

完整配置、重试规则、管理 API、旧版迁移、构建与排障见[详细指南](docs/guide.zh-CN.md)。容器部署请将 `state_file` 指向持久卷中的可写路径。

日期安排通过“保存设置”生效。排除日期与“明天唤醒”互斥，保存为所选时区的具体日期，到期自动恢复原每日时点，不补发跳过的任务。节假日数据按提莫、holiday-cn GitHub、jsDelivr 顺序获取；全部失败时使用缓存，无该年缓存时只按普通周末判断并显示异常提示。手动导入优先于在线数据，缓存文件位于 `state_file + ".holidays.json"`，应一并放在持久卷中。

## 周额度与五小时额度调度（v0.3.0）

以下规则对应当前源码及包含该实现的构建包；商店安装版本以 `registry.json` 指向的已发布构建为准。调度按 CPA 8.0.33 的 `scheduler.pick` 协议实现，建议使用该版本或更新版本。

在插件页面勾选 **“按周和五小时剩余额度均衡分配”**，保存后启用。额度调度默认关闭；开启后为正常 Codex 请求选择账号，定时唤醒仍执行其单独的计划。

### 账号选择顺序

1. 从 CPA 提供的候选账号中，排除不可用状态及不符合下游 Key 绑定规则的账号。
2. 排除任一**已知且仍有效**的五小时或周窗口已用量达到 100% 的账号。全部耗尽时返回 `quota_exhausted`，不再分配已耗尽账号。
3. 新建或重建会话绑定时，在剩余账号中使用最高账号优先级。高优先级账号额度耗尽后，可使用较低优先级的可用账号。
4. 先保留周剩余量距最高值不超过 **5 个百分点**的账号，再在该组中保留五小时剩余量距最高值不超过 **5 个百分点**的账号，最后轮询。轮询按下游 Key、提供商和模型隔离。

例如下面的三个账号优先级相同，页面显示的百分比均为**剩余额度**：

| 账号 | 周剩余 | 五小时剩余 | 新请求分配 |
| --- | --- | --- | --- |
| Business Premium | 68% | 上游未提供 | 周余量较低，暂不优先 |
| Plus A | 98% | 94% | 当前优先使用 |
| Plus B | 96% | 76% | 周余量接近，但五小时余量较低 |

Plus A、Plus B 的周余量相差 2 个百分点，都会进入五小时比较；五小时余量相差 18 个百分点，因此当前优先使用 Plus A。余量变化后重新比较；两个窗口的差距都在 5 个百分点以内时轮询，逐步平衡额度消耗，不强求三个账号的请求数相同。

按实际时长识别 **18000 秒**五小时窗口和 **604800 秒**周窗口，不假设 `primary_window` 一定是五小时。Business Premium 仅返回周额度时也参与调度；缺少窗口、观测过期或查询失败时保留为未知候选，不当作已用 0%，也不因缺少五小时窗口直接排除。

### 已有会话何时切换

额度接近时继续使用已绑定账号。以下情况会在下一次请求重新选择账号并更新绑定：

- 原账号不再出现在可用候选中，或缓存确认任一额度窗口已耗尽。
- 与同提供商、同优先级的可用账号相比，周已用量比最低值高出**超过 10 个百分点**。
- 原账号仍处于上述周用量范围内，但五小时已用量比该组最低值高出**超过 20 个百分点**。

周和五小时的重新分配阈值比新会话宽，减少余量微小变化带来的频繁切换。**5 / 10 / 20 个百分点是当前代码中的固定阈值，不是 YAML 配置项。** 不会在单次请求执行到一半时切换；一次长请求仍消耗同一个账号。

显式指定账号的请求（包括定时唤醒）优先遵守宿主的 `pinned_auth_id`；额度路由账号池独立于唤醒的 `auth_ids`，Plus 唤醒限制不排除 Business Premium 参与正常请求调度。未绑定 Key 的非 Codex 或混合提供商请求交还 CPA；显式 Key 绑定仍限制账号范围。没有稳定会话标识的 Codex 请求按相同额度规则逐次分配，不建立会话绑定。

### 配置、缓存与状态

也可在 CPA 配置的 `plugins.configs.cpa-codex-5h-reset` 中添加：

```yaml
quota_routing:
  enabled: true
  session_ttl: 4h
  quota_cache_ttl: 1m
  # state_file: /persistent/path/routing.json  # 默认 state_file + ".routing.json"
```

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `false` | 是否启用额度调度 |
| `session_ttl` | `4h` | 会话从最后一次请求起的保留时间，范围 `1m` 至 `168h` |
| `quota_cache_ttl` | `1m` | 额度缓存时间，范围 `1s` 至 `5m` |
| `state_file` | 唤醒 `state_file` 加 `.routing.json` | 持久化会话绑定及轮询位置，需为可写路径 |
| `bindings` | `[]` | 可选的下游 Key 与允许账号规则 |
| `unbound` | `passthrough` | 未绑定 Key 默认走额度调度；`deny` 则拒绝 |

已有会话也会在缓存到期后刷新额度；如果其他请求正在刷新，先使用缓存，下一次请求再参考新观测。任一已知窗口到期也会触发重新查询。会话绑定及轮询位置支持重启恢复，兼容旧版绑定文件；状态文件不含明文 Key 或会话 ID。

管理 API 的 `/status` 中，`quota_routing.strategy` 为 `weekly-then-five-hour`，并提供缓存额度、活跃绑定数和持久化错误。额度对象中的 `known`、`used_percent`、`reset_at` 描述五小时窗口，`weekly` 描述周窗口；仅有周窗口时 `known: false` 不表示周额度未知。页面将已用百分比转换为剩余百分比显示。

### 迁移下游 Key 绑定

**使用 `key-account-bind` 时先迁移规则。** CPA 只使用最高优先级的一个调度插件，不能将两个调度器串联。将原插件的对象形式 `bindings` 和 `unbound` 复制到本插件的 `quota_routing` 中，再禁用原调度插件，并确保本插件的 `priority` 高于其他调度器。例如：

```yaml
quota_routing:
  enabled: true
  bindings:
    - key: "下游专用 Key A"
      allow: ["codex-account-a.json"]
    - key: "下游专用 Key B"
      allow: ["codex-account-b.json"]
  unbound: passthrough
```

绑定规则支持大小写不敏感的账号 ID 通配符，Key 本身区分大小写。绑定账号不可用时请求失败，不会逃逸到其他账号；未绑定 Key 默认使用额度路由，可用 `unbound: deny` 禁止。绑定 Key 必须通过 Authorization Bearer 或 API-Key 请求头传递；内部指定账号的唤醒不受此限制。

客户端应提供 `Session_id`、`Session-Id`、`X-Codex-Thread-Id`、`X-Session-ID` 或 `X-Conversation-ID`；也支持 CPA 提供的会话元数据。绑定按下游 Key、提供商、模型和会话隔离，状态文件不保存明文 Key 或会话 ID。首次启用时无法读取 CPA 原先的内存绑定，旧会话的首次请求会建立新的插件绑定；应在旧任务结束后切换，或提前迁移已知绑定。状态文件损坏会拒绝启用路由，新绑定写入失败会拒绝该次选号，已有绑定可继续在内存中运行并显示状态错误。调度根据额度观测逐步平衡消耗，不保证请求数或消耗百分比时刻相同；单次长请求的消耗仍集中于一个账号。升级后恢复已有绑定，并在后续请求按上述阈值重新分配。

原有 ABI 1 / schema 6 唤醒接口保持兼容。

## 项目结构

```text
cpa-codex-5h-reset/
├── main.go                 # 原生插件入口与 ABI
├── host.go                 # CPA 宿主接口调用
├── management.go           # 管理 API 与嵌入式页面资源
├── internal/keeper/        # 调度、额度查询、重试与状态持久化
├── internal/routing/       # 双窗口额度调度、会话绑定与 Key 隔离
├── web/                    # 管理页面与前端测试
├── scripts/                # 安装、通知与 ABI 冒烟测试脚本
├── docs/                   # 详细使用指南
├── .github/workflows/      # 跨平台构建与发布
├── registry.json           # CPA 插件商店源
├── README.md               # 中文说明（默认）
├── README_EN.md            # 英文说明
└── LICENSE                 # MIT 许可证
```

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=BIMiracle/cpa-codex-5h-reset&type=Date)](https://star-history.com/#BIMiracle/cpa-codex-5h-reset&Date)

## 许可证

本项目基于 [MIT License](LICENSE) 开源。
