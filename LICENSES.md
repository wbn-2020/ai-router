# 第三方许可证与来源说明

本项目是 clean-room 实现：只读取参考项目以研究协议、交互与运维模式，不复制、
不 fork、不引入参考项目的任何源码或构建产物。`reference/` 不参与构建，也不进入
许可证与 clean-room 自动扫描范围。

> 自动扫描只能发现明显的复制、导入与依赖痕迹，不能替代人工许可证、来源与代码审计。
> 发布前仍需人工复核直接/间接依赖、前端构建依赖与本文件的一致性。

## 参考项目（只读研究）

| 项目 | 许可证 | 使用方式与边界 |
| --- | --- | --- |
| sub2api | LGPL-3.0-or-later | 仅只读参考协议与交互；不复制代码、不使用其 Vue 前端或 PostgreSQL/Redis 运行时。 |
| new-api | AGPLv3 | 仅只读参考协议与管理端信息层级；不复制代码、不使用其多租户/支付能力。 |
| juhe-ai | Apache-2.0 | 仅只读参考运维与上游适配思路；不复制代码、不使用其 Vue + PostgreSQL/Redis 运行时。 |
| claude-code-hub | MIT | 仅只读参考 Codex/Responses 交互；不复制代码、不使用其 Next.js + PostgreSQL/Redis 运行时。 |

## Go 直接依赖

| 模块 | 版本 | 许可证 | 用途 |
| --- | --- | --- | --- |
| modernc.org/sqlite | v1.47.0 | BSD-3-Clause | 纯 Go SQLite 驱动（CGO_ENABLED=0），由 T02 数据库层使用。 |
| Go 标准库 | go1.26 | BSD-3-Clause | 配置、HTTP、加密、数据库接口等，不产生外部模块依赖。 |

## Go 间接依赖

以下模块由 `modernc.org/sqlite` 引入，版本与 `go.mod`/`go.sum` 对齐；升级 SQLite
驱动时必须同步复核本表。

| 模块 | 版本 | 许可证 |
| --- | --- | --- |
| github.com/dustin/go-humanize | v1.0.1 | MIT |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause |
| github.com/mattn/go-isatty | v0.0.20 | MIT |
| github.com/ncruces/go-strftime | v1.0.0 | MIT |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause |
| golang.org/x/sys | v0.42.0 | BSD-3-Clause |
| modernc.org/libc | v1.70.0 | BSD-3-Clause |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause |
| modernc.org/memory | v1.11.0 | BSD-3-Clause |

## 禁止依赖

本项目不使用 Redis、消息队列、微服务、多租户或 PostgreSQL/MySQL 等额外存储。
`go.mod`/`go.sum` 由 compliance 测试检查，出现禁用依赖即失败。
