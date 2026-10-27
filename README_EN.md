# CPA Codex 5h Reset

**Scheduled Codex wake-up requests · A native CLIProxyAPI (CPA) plugin**

[中文](README.md) | English

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/BIMiracle/cpa-codex-5h-reset)](https://github.com/BIMiracle/cpa-codex-5h-reset/releases)

Send lightweight requests to your Codex Plus accounts in CPA on a daily schedule. After a failure, the plugin checks the five-hour quota reset time and waits before retrying. Use the management page to configure schedules, inspect quotas, and trigger requests manually. Supports Windows, macOS, and Linux.

The plugin can also select credentials for CPA requests, comparing **weekly headroom first, then five-hour headroom**. It supports mixed Plus and Business Premium accounts, session bindings, and rebinding when quota usage diverges. See [quota routing](#weekly-and-five-hour-quota-routing-v030).

Five-hour wake requests apply only to **Plus**. **Business Premium**, other plans, and accounts with an unknown plan are excluded from scheduled requests, manual requests, and retries. Pending tasks are cancelled when an ineligible plan is detected. Quota queries and quota routing continue to operate independently.

> The plugin cannot force an upstream quota reset. Wake-up requests consume a small amount of quota, and a successful request does not prove that the quota has reset. Your computer and CPA must remain running.

## Features

- **Scheduled requests**: Configure a time zone and daily schedules for all or selected Codex Plus accounts, with no extra downstream API key.
- **Quota routing**: Select available credentials by weekly and five-hour headroom, rotate when quotas are close, and rebalance established sessions when usage diverges.
- **Date rules**: Select a time zone (default `Asia/Singapore`), skip mainland Chinese holidays and ordinary weekends while retaining makeup workdays, exclude one date range within the next 30 days, or override rest-day rules for tomorrow only.
- **Holiday calendar**: A six-week month grid from Monday to Sunday, including adjacent-month dates and navigation to the previous, current and next month. Background fetching from multiple sources, persistent annual caches, and JSON import with a preview. Manual wake requests remain available during excluded dates and rest days.
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
2. The page automatically reuses the management center's current login session to load accounts, five-hour and weekly remaining quotas, and settings. No second management key entry is needed.
3. Select accounts, a model, a time zone, and daily schedules. Save, then trigger a manual request to verify task status.
4. For browser notifications, click “开启网页通知” (Enable web notifications), grant permission, and keep the page open.

The plugin follows the management center's theme menu automatically. Standalone pages reuse the same-origin theme preference and remembered login; open the plugin from the logged-in management center for memory-only sessions. Usage is queried on opening, manual refresh, and once per minute without triggering a wake request.

If the plugin page returns **404**, confirm that the running CPA process has successfully loaded the plugin; setting `enabled: true` alone is insufficient. Check the configuration file used at startup, `plugins.dir`, and the loading logs, then reload the plugin or restart CPA. See [troubleshooting (Chinese)](docs/guide.zh-CN.md#故障排查).

| Setting | Default |
| --- | --- |
| Wake accounts | All enabled Codex Plus accounts, including Plus accounts added later |
| Model / reasoning effort | `gpt-6-luna` / `low`; must be supported by your CPA instance and account |
| Time zone | `Asia/Singapore` |
| Holiday skipping / one-time rule | Off / default rules |
| Daily schedule | `05:00`, `10:01`, `15:02`, `20:03` |
| Maximum retries | `3`, in addition to the initial request |
| Desktop notifications | Off; enable with `desktop_notifications` |
| Quota routing | Off; enable with `quota_routing.enabled`. Its account pool is independent of wake account selection |

See the [detailed guide (Chinese)](docs/guide.zh-CN.md) for full configuration, retry behavior, management APIs, migration, building, and troubleshooting. In containers, set `state_file` to a writable path on a persistent volume.

Date settings take effect when saved. Exclusion and tomorrow wake are mutually exclusive and use fixed dates in the selected time zone, so they do not repeat after restarting. Expired rules restore the daily schedule without replaying skipped requests. Holiday fetching tries Timor, holiday-cn on GitHub, then jsDelivr. Failed downloads retain cached data; without an annual cache, only ordinary weekends are skipped and the page shows a warning. Imported annual data takes precedence until cleared. Keep `state_file + ".holidays.json"` on the persistent volume alongside the task state.

## Weekly and five-hour quota routing (v0.3.0)

These rules describe the current source and builds that include this implementation. Store installations use the published build referenced by `registry.json`. Routing implements CPA 8.0.33's `scheduler.pick` protocol; use that version or newer.

Enable **“按周和五小时剩余额度均衡分配”** on the plugin page and save. Routing is off by default. When enabled, it selects credentials for normal Codex requests; wake requests continue to follow their separate schedules.

### Credential selection order

1. Start with CPA's available candidates and exclude unusable credentials or credentials outside the downstream key's allowed pool.
2. Exclude a credential if any **known, current** five-hour or weekly window has reached 100% usage. An exhausted pool returns `quota_exhausted`.
3. When creating or replacing a session binding, use the highest remaining credential priority. Lower priorities can take over after higher-priority credentials exhaust their quota.
4. Keep credentials within **five percentage points** of the highest weekly remaining quota. Within that group, apply the same filter to five-hour remaining quota, then rotate. Rotation is scoped by downstream key, provider, and model.

For example, these three accounts have the same priority. All percentages below show **remaining quota**:

| Account | Weekly remaining | Five-hour remaining | New request selection |
| --- | --- | --- | --- |
| Business Premium | 68% | Not provided upstream | Lower weekly headroom; temporarily deprioritized |
| Plus A | 98% | 94% | Preferred now |
| Plus B | 96% | 76% | Similar weekly headroom, lower five-hour headroom |

Both Plus accounts pass the weekly filter because their headroom differs by two points. Their five-hour headroom differs by eighteen points, so Plus A is preferred. Reevaluate as quotas change; rotate when both differences fall within five points. The aim is gradual quota balancing, without requiring equal request counts.

Windows are identified by actual duration: **18000 seconds** for five hours and **604800 seconds** for a week. `primary_window` is not assumed to mean five hours. A Business Premium credential exposing only weekly quota still participates. Missing windows, stale observations, and failed queries remain unknown candidates, rather than being assigned zero usage or excluded for lacking a five-hour window.

### When established sessions move

Keep the existing credential while quotas are close. Rebind on the next request when:

- The original credential leaves the available pool, or cached quota confirms that either window is exhausted.
- Weekly usage exceeds the lowest available peer of the same provider and priority by **more than ten percentage points**.
- The credential remains in that weekly cohort, but its five-hour usage exceeds the cohort's minimum by **more than twenty percentage points**.

The wider rebinding thresholds reduce frequent switches caused by small changes. **The 5/10/20-point thresholds are constants in the current implementation, not YAML options.** Credentials never change in the middle of an executing request; one long request still consumes one account.

Explicit host `pinned_auth_id` requests, including wake requests, take precedence. Routing is independent of wake `auth_ids`: the Plus-only wake restriction does not exclude Business Premium from normal request routing. Unbound non-Codex or mixed-provider requests retain native CPA scheduling; explicit downstream key bindings still restrict their credential pool. Codex requests without stable session identity use the same quota rules per request without creating bindings.

### Configuration, caching, and status

Add this under `plugins.configs.cpa-codex-5h-reset`:

```yaml
quota_routing:
  enabled: true
  session_ttl: 4h
  quota_cache_ttl: 1m
  # state_file: /persistent/path/routing.json  # defaults to wake state_file + ".routing.json"
```

| Setting | Default | Behavior |
| --- | --- | --- |
| `enabled` | `false` | Enable quota routing |
| `session_ttl` | `4h` | Inactivity lifetime for session bindings; `1m` to `168h` |
| `quota_cache_ttl` | `1m` | Quota cache lifetime; `1s` to `5m` |
| `state_file` | Wake `state_file` plus `.routing.json` | Writable path for persisted bindings and rotation cursors |
| `bindings` | `[]` | Optional downstream keys and allowed credential pools |
| `unbound` | `passthrough` | Unbound keys use quota routing; `deny` rejects them |

Established sessions also refresh expired quota caches. While another request is already refreshing, they use cached observations and consider the new data on a subsequent request. An expired known window also triggers a refresh. Bindings and rotation cursors survive restarts, support older binding files, and contain no raw downstream keys or session IDs.

The management `/status` response exposes `quota_routing.strategy: weekly-then-five-hour`, cached quota, active binding count, and persistence errors. In quota objects, `known`, `used_percent`, and `reset_at` describe the five-hour window; `weekly` describes the weekly window. A weekly-only account can have `known: false` with valid weekly data. The page converts used percentages to remaining percentages.

### Migrating downstream key bindings

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

Supported identity headers include `Session_id`, `Session-Id`, `X-Codex-Thread-Id`, `X-Session-ID`, and `X-Conversation-ID`, with CPA session metadata as fallback. Bindings are isolated by downstream key, provider, model, and session. State never contains raw keys or session IDs. On first enable, CPA's previous in-memory bindings cannot be read: finish old tasks before switching, or migrate known bindings beforehand. Invalid state prevents routing activation; failed persistence rejects new bindings while existing bindings can continue in memory and expose a status error. Quota observations guide gradual balancing, without guaranteeing equal request counts or usage percentages at every instant. A single long request still consumes one account. Upgrades restore existing bindings and apply rebinding thresholds on subsequent requests.

Wake ABI 1 / schema 6 remains compatible.

## Project Structure

```text
cpa-codex-5h-reset/
├── main.go                 # Native plugin entry point and ABI
├── host.go                 # CPA host interface calls
├── management.go           # Management API and embedded page resources
├── internal/keeper/        # Scheduling, quota checks, retries, and persistence
├── internal/routing/       # Dual-window quota routing, session bindings, and key isolation
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
