# CPA Codex 5h Reset

**Scheduled Codex wake-up requests · A native CLIProxyAPI (CPA) plugin**

[中文](README.md) | English

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/BIMiracle/cpa-codex-5h-reset)](https://github.com/BIMiracle/cpa-codex-5h-reset/releases)

Send lightweight requests to your Codex accounts in CPA on a daily schedule. After a failure, the plugin checks the five-hour quota reset time and waits before retrying. Use the management page to configure schedules, inspect quotas, and trigger requests manually. Supports Windows, macOS, and Linux.

> The plugin cannot force an upstream quota reset. Wake-up requests consume a small amount of quota, and a successful request does not prove that the quota has reset. Your computer and CPA must remain running.

## Features

- **Scheduled requests**: Configure a time zone and daily schedules for all or selected Codex accounts, with no extra downstream API key.
- **Bounded retries**: After failure, wait for the five-hour quota reset when known; otherwise, retry after 30 seconds. Up to 3 retries by default.
- **Management page**: View account quotas, reset times, task status, and logs, or trigger requests manually.
- **Recovery and alerts**: Persist task state and resume unfinished tasks after a restart. Receive web alerts and optional desktop notifications on final failure.

## Installation and Usage

### 1. Prerequisites

CPA must have authenticated Codex accounts and support native plugins with **ABI 1 / schema 6**, management resources, and `host.model.execute`, `host.auth.get`, and `host.http.do`. Loading has been verified on Windows with **CPA 8.0.4 (d33f63f8)**; check compatibility for other versions.

### 2. Install the plugin

Add this custom source in the plugin store on the CPA [management page](http://localhost:8317/management.html):

```text
https://raw.githubusercontent.com/BIMiracle/cpa-codex-5h-reset/main/registry.json
```

Install and enable **Codex 五小时重置唤醒**. If the page has no option to add a source, merge the following into the existing `plugins` block in your CPA configuration:

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/BIMiracle/cpa-codex-5h-reset/main/registry.json
```

Store installation requires a matching published release. Alternatively, download the ZIP matching your **CPA process architecture** from [Releases](https://github.com/BIMiracle/cpa-codex-5h-reset/releases), verify its SHA256 against `checksums.txt`, and place the library in the appropriate directory under `plugins.dir`. Restart CPA or reload plugins:

| Platform | Architecture | Library | Directory |
| --- | --- | --- | --- |
| Windows | amd64 | `cpa-codex-5h-reset.dll` | `windows/amd64/` |
| macOS | amd64 / arm64 | `cpa-codex-5h-reset.dylib` | `darwin/<arch>/` |
| Linux | amd64 / arm64 | `cpa-codex-5h-reset.so` | `linux/<arch>/` |

Linux prebuilt packages require glibc; build from source for Alpine/musl. Full manual configuration and Windows installer instructions are available in the [detailed guide (Chinese)](docs/guide.zh-CN.md#配置与管理页面).

### 3. Configure and run

1. Open the plugin page from the CPA plugin menu, or visit the [plugin management page](http://localhost:8317/v0/resource/plugins/cpa-codex-5h-reset/ui). Replace the host and port for remote deployments.
2. Connect with your **CPA Management Key**, which is kept only in the current page's memory.
3. Select accounts, a model, a time zone, and daily schedules. Save, then trigger a manual request to verify task status.
4. For browser notifications, click “开启网页通知” (Enable web notifications), grant permission, and keep the page open.

If the plugin page returns **404**, confirm that the running CPA process has successfully loaded the plugin; setting `enabled: true` alone is insufficient. Check the configuration file used at startup, `plugins.dir`, and the loading logs, then reload the plugin or restart CPA. See [troubleshooting (Chinese)](docs/guide.zh-CN.md#故障排查).

| Setting | Default |
| --- | --- |
| Accounts | All enabled Codex accounts, including accounts added later |
| Model / reasoning effort | `gpt-6-luna` / `low`; must be supported by your CPA instance and account |
| Time zone | `Asia/Singapore` |
| Daily schedule | `05:00`, `10:01`, `15:02`, `20:03` |
| Maximum retries | `3`, in addition to the initial request |
| Desktop notifications | Off; enable with `desktop_notifications` |

See the [detailed guide (Chinese)](docs/guide.zh-CN.md) for full configuration, retry behavior, management APIs, migration, building, and troubleshooting. In containers, set `state_file` to a writable path on a persistent volume.

## Route new sessions by remaining quota (v0.3.0)

Enable **“新会话优先使用五小时剩余额度更多的账号”** on the plugin page and save. This is opt-in so existing installations keep their current scheduler until configured.

- New Codex sessions choose the lowest five-hour used percentage among available credentials in the highest priority tier. Equal percentages use round-robin.
- Established sessions retain their credential even when another account later has more headroom. If CPA removes the credential from available candidates, the session fails over and updates its binding.
- Quota is cached for one minute and refreshed when its window expires. Bindings expire after four hours of inactivity and are persisted for restart recovery. Known, non-exhausted quota is preferred; if all observations are unknown, selection falls back to round-robin.
- Explicit auth pins, including wake requests, take precedence. Unbound non-Codex routes and requests without stable session identity retain native scheduling. Wake `auth_ids` do not restrict the routing pool.

Add this under `plugins.configs.cpa-codex-5h-reset` to configure it directly:

```yaml
quota_routing:
  enabled: true
  session_ttl: 4h
  quota_cache_ttl: 1m
  # state_file: /persistent/path/routing.json  # defaults to wake state_file + ".routing.json"
```

**Migrate `key-account-bind` before enabling.** CPA activates only its highest-priority scheduler; schedulers are not chained. Copy the old plugin's object-form `bindings` and `unbound` into `quota_routing`, disable the old scheduler, and give this plugin a higher `priority` than competing schedulers:

```yaml
quota_routing:
  enabled: true
  bindings:
    - key: "downstream-dedicated-key-a"
      allow: ["codex-account-a.json"]
    - key: "downstream-dedicated-key-b"
      allow: ["codex-account-b.json"]
  unbound: passthrough
```

Credential globs are case-insensitive; keys remain case-sensitive. A bound key fails closed when no allowed credential is available. Unbound keys use quota routing by default; `unbound: deny` rejects them instead. Bound keys must authenticate using a Bearer/API-Key header; internal pinned wake requests are exempt.

Supported identity headers include `Session_id`, `Session-Id`, `X-Codex-Thread-Id`, `X-Session-ID`, and `X-Conversation-ID`, with CPA session metadata as fallback. Bindings are isolated by downstream key, provider, model, and session. State never contains raw keys or session IDs. On first enable, CPA's previous in-memory bindings cannot be read: finish old tasks before switching, or migrate known bindings beforehand. Invalid state prevents routing activation; failed persistence rejects new bindings while existing bindings can continue in memory and expose a status error. Long sessions can still produce uneven account consumption.

Implemented against CPA 8.0.33's scheduler protocol; use that version or newer. Wake ABI 1 / schema 6 stays unchanged. `/status` includes a `quota_routing` object with cached quotas, active binding count, and persistence errors.

## Project Structure

```text
cpa-codex-5h-reset/
├── main.go                 # Native plugin entry point and ABI
├── host.go                 # CPA host interface calls
├── management.go           # Management API and embedded page resources
├── internal/keeper/        # Scheduling, quota checks, retries, and persistence
├── web/                    # Management page and frontend tests
├── scripts/                # Installation, notifications, and ABI smoke tests
├── docs/                   # Detailed usage guide
├── .github/workflows/      # Cross-platform builds and releases
├── registry.json           # CPA plugin store source
├── README.md               # Chinese README (default)
├── README_EN.md            # English README
└── LICENSE                 # MIT license
```

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=BIMiracle/cpa-codex-5h-reset&type=Date)](https://star-history.com/#BIMiracle/cpa-codex-5h-reset&Date)

## License

This project is licensed under the [MIT License](LICENSE).
