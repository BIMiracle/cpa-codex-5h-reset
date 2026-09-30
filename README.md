# CPA Codex 5h Reset

**Codex 五小时重置唤醒 · CLIProxyAPI（CPA）原生插件**

中文 | [English](README_EN.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/BIMiracle/cpa-codex-5h-reset)](https://github.com/BIMiracle/cpa-codex-5h-reset/releases)

为 CPA 中的 Codex 账号定时发送轻量请求；失败后查询五小时额度重置时间，等待重置后重试。通过管理页面即可设置计划、查看额度和手动唤醒，支持 Windows、macOS 和 Linux。

> 插件不能强制重置上游额度。唤醒请求会消耗少量额度，请求成功不代表额度已重置；电脑与 CPA 需要保持运行。

## 主要功能

- **定时唤醒**：自定义时区和每日计划，支持全部或指定 Codex 账号，无需额外的下游 API Key。
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
2. 输入 **CPA Management Key** 连接，密钥仅保存在当前页面内存中。
3. 选择账号、模型、时区与每日计划，保存后点击手动唤醒，确认任务状态。
4. 如需网页通知，点击“开启网页通知”并允许权限，保持页面打开。

若插件页面返回 **404**，先确认当前 CPA 进程已成功加载插件，而不只是配置中写了 `enabled: true`。检查启动时使用的配置文件、`plugins.dir` 与加载日志，再重新加载插件或重启 CPA；详见[故障排查](docs/guide.zh-CN.md#故障排查)。

| 设置 | 默认值 |
| --- | --- |
| 账号 | 所有启用的 Codex 账号，包括以后新增的账号 |
| 模型 / 推理档 | `gpt-6-luna` / `low`，须为当前 CPA 和账号支持的值 |
| 时区 | `Asia/Singapore` |
| 每日计划 | `05:00`、`10:01`、`15:02`、`20:03` |
| 最大重试次数 | `3`，即首次请求之外最多再试 3 次 |
| 桌面通知 | 默认关闭，可通过 `desktop_notifications` 开启 |

完整配置、重试规则、管理 API、旧版迁移、构建与排障见[详细指南](docs/guide.zh-CN.md)。容器部署请将 `state_file` 指向持久卷中的可写路径。

## 项目结构

```text
cpa-codex-5h-reset/
├── main.go                 # 原生插件入口与 ABI
├── host.go                 # CPA 宿主接口调用
├── management.go           # 管理 API 与嵌入式页面资源
├── internal/keeper/        # 调度、额度查询、重试与状态持久化
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
