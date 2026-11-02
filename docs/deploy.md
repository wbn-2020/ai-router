# 部署与运维

本文档覆盖本机运行、Linux/Docker 部署、反向代理 TLS、数据迁移、备份/恢复与迁移失败处理。
目标是单进程、单管理员、单客户端 Key；不需要 Redis、消息队列或额外数据库。

> 自动验收只覆盖 Go 测试与 Dockerfile 静态契约，**不会真实执行 `docker build` 或启动容器**。
> 发布前必须按文末「人工验收清单」实际构建并运行一次镜像。

## 1. 前置条件

- Go 1.26（本机直接构建时）。
- Node.js 24 / npm 11（只有修改管理端或从源码构建二进制时需要）。
- Docker 24+（可选，用于容器部署）。

管理端 SPA 通过 Go `embed` 编译进二进制。源码构建时如果 `internal/webassets/dist`
还是占位页，二进制里也只有占位页；因此先构建前端再构建后端：

```bash
npm --prefix web ci
npm --prefix web run build
mkdir -p bin
go build -trimpath -o bin/gateway ./cmd/gateway
```

`web/vite.config.ts` 已把 Vite 的 `outDir` 固定为 `internal/webassets/dist`，
产物位于 `internal/webassets/dist/assets/index-<hash>.js`。

## 2. 本机运行

### 2.1 生成主密钥与客户端 Key

```bash
./bin/gateway keygen
```

输出两行，形如：

```text
GATEWAY_MASTER_KEY=<base64 32 字节>
GATEWAY_CLIENT_KEY=gw_<随机串>
```

把这两行保存到本机环境变量或权限为 `0600` 的环境文件。主密钥必须稳定并离线备份：
上游渠道 Key 使用它加密，丢失后数据库里的上游 Key 无法解密。

### 2.2 启动

```bash
export GATEWAY_MASTER_KEY='...'
export GATEWAY_CLIENT_KEY='...'
export GATEWAY_ADMIN_PASSWORD='至少 12 个字符的初始密码'
./bin/gateway serve
```

默认只监听 `127.0.0.1:8080`，管理端地址为 <http://127.0.0.1:8080/admin>。
`GATEWAY_ADMIN_PASSWORD` 只在数据库还没有管理员记录时用于首次创建；之后改密在管理端完成。
启动后可以从环境变量中移除该密码。

启动时会自动执行数据库迁移。缺少主密钥、缺少客户端 Key、监听地址非法、迁移失败
都会导致进程以非零状态退出，而不是带着未知配置继续服务。提供 HTTP 服务前还会只读验证
全部现有渠道（含停用渠道）的密文可解密；失败报告“加密状态不可读”并停止启动。
该错误可能来自密钥不匹配或密文损坏，不会自动删除、重加密或修复数据。空库可正常初始化；
此检查不代表对历史捕获及备份完成了全库密钥审计。

## 3. 环境变量

下表默认值与 `internal/config/config.go` 一致。所有时长使用 Go 时长语法（`30s`、`5m`）。

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GATEWAY_LISTEN_ADDR` | `127.0.0.1:8080` | HTTP 监听地址；容器镜像内默认覆盖为 `0.0.0.0:8080`。 |
| `GATEWAY_DATA_DIR` | `data` | 数据目录；数据库与备份都位于其下，不依赖进程当前目录。 |
| `GATEWAY_DB_PATH` | `<GATEWAY_DATA_DIR>/gateway.db` | 数据库文件路径；一般不设置。 |
| `GATEWAY_MASTER_KEY` | 无（必填） | base64 编码的 32 字节密钥；格式错误拒绝启动。 |
| `GATEWAY_CLIENT_KEY` | 无（必填） | 客户端统一 Key，至少 24 个字符。 |
| `GATEWAY_ADMIN_PASSWORD` | 空 | 仅首次创建管理员时使用，至少 12 个字符；已有管理员时忽略。 |
| `GATEWAY_COOKIE_SECURE` | `false` | 管理端 session cookie 是否带 `Secure`；生产 TLS 环境设为 `true`。 |
| `GATEWAY_CODEX_CONTEXT_WINDOW` | `128000` | Codex 模型 manifest 的上下文窗口。 |
| `GATEWAY_UPSTREAM_CONNECT_TIMEOUT` | `10s` | 上游 TCP 连接超时。 |
| `GATEWAY_UPSTREAM_TLS_TIMEOUT` | `10s` | 上游 TLS 握手超时。 |
| `GATEWAY_UPSTREAM_HEADER_TIMEOUT` | `180s` | 等待上游响应头的超时。 |
| `GATEWAY_UPSTREAM_STREAM_IDLE_TIMEOUT` | `300s` | 流式读取空闲超时。 |
| `GATEWAY_UPSTREAM_TIMEOUT` | `300s` | 每次非流式上游尝试的上限。 |
| `GATEWAY_UPSTREAM_STREAM_TIMEOUT` | `15m` | 每次流式上游尝试的上限。 |
| `GATEWAY_REQUEST_TIMEOUT` | 继承 `GATEWAY_UPSTREAM_TIMEOUT` | 非流式转发总预算，包含候选等待和所有尝试。 |
| `GATEWAY_STREAM_TIMEOUT` | 继承 `GATEWAY_UPSTREAM_STREAM_TIMEOUT` | 流式转发总预算，包含候选等待和所有尝试。 |
| `GATEWAY_FIRST_CONTENT_TIMEOUT` | `180s` | 流式首有效语义输出预算；心跳不刷新，提交后撤除。 |
| `GATEWAY_DOWNSTREAM_WRITE_TIMEOUT` | `30s` | 每次下游写出/Flush的可续期上限，同时受转发总预算约束。 |
| `GATEWAY_PROBE_TIMEOUT` | `60s` | 管理端指定模型的单次生成检查总预算。 |
| `GATEWAY_MODEL_DISCOVERY_TIMEOUT` | `30s` | 管理端模型发现总预算。 |
| `GATEWAY_MAX_ATTEMPTS` | `3` | 单次请求最多尝试的渠道数，取值 1-3。 |
| `GATEWAY_CAPACITY_WAIT` | `2s` | 全部候选渠道满载时的最长等待；`0` 表示不等待。 |
| `GATEWAY_CAPTURE_MAX_BYTES` | `8388608` | 单侧（请求或响应）诊断捕获上限，8 MiB；诊断环总上限仍为 32 MiB。 |

总预算从进入转发编排开始计算，不包含入站请求体上传。上游子超时与总预算同时生效，以先到者为准；客户端更短的截止也优先。成功响应及已提交流的写截止不超过总预算。尚未提交时，预算耗尽后的固定错误JSON最多使用额外 `min(1s, GATEWAY_DOWNSTREAM_WRITE_TIMEOUT)` 写窗口，不再启动上游尝试；已提交流不能改成HTTP 504或无缝切换。检查结果落库另有最多1秒的记录预算，不延长检查请求。请求完成后的日志持久化有独立有限收尾时间。

### 3.2 模型能力与有状态请求

- 每条模型映射可以声明 `input_modalities`（unknown/text/text_image）、`context_window` 与 `reasoning_levels`（none/low/medium/high/xhigh/max/ultra）。未声明表示“未知”，不是“不支持”。
- Codex manifest 与图片准入使用同一别名全部同协议候选的**共同能力**：任一候选显式 `text` 时别名收窄为纯文本并拒绝图片；窗口取已声明候选的最小值（不超过全局配置）；推理档位取交集，未声明或交集为空回退 `none`。
- 未声明模态默认按 `text+image`（保持图片能力，管理端需醒目标注“未声明”）；如果确认某上游只支持文本，请显式声明 `text`。
- `background=true` 一律返回 400 `unsupported_parameter`：网关没有后台响应生命周期端点。
- `previous_response_id`、`conversation`、`reasoning.encrypted_content`、`item_reference` 视为有状态请求：仅当该别名恰有 1 条启用同协议候选时放行并钉住该渠道、禁止跨渠道重试；多候选返回 400 `stateful_route_unavailable`。
- `GET/DELETE/cancel /v1/responses/{id}` 与 Conversations API 仍未实现（404）；conversation id 只能在网关之外创建。
- 有状态请求的上游失败按请求范围记录，不写渠道熔断。

### 3.3 渠道运行状态与检查

- **启用状态**控制是否参与调度；**运行状态**来自当前进程的并发与熔断快照；**最近检查**是管理员显式触发的指定模型/协议生成检查。三者不互相替代。
- 冷却结束但尚未成功恢复时显示待恢复/恢复中。仅一个生产请求可取得恢复许可，不自动发起付费后台探针；进程重启会清空运行态。
- 模型级故障只影响对应渠道、协议和上游模型；明确请求错误及客户端取消不处罚渠道。未知错误使用保守分类，错误信息不展示原始上游正文。
- 检查保持单次、16输出token上限。返回有效生成才显示通过；Responses incomplete或Chat length显示不确定；HTTP 200、模型列表出现、一次生成通过都不等于工具/图片/质量认证。
- 响应头耗时与完整检查耗时分开。修改URL、协议或显式更换Key会使旧检查失效；迟到的旧版本结果不能覆盖新配置。仅改名不失效。

## 4. 数据目录与迁移

`GATEWAY_DATA_DIR` 是唯一的状态根目录：

```text
<GATEWAY_DATA_DIR>/
├── gateway.db                 # SQLite（WAL 模式）
├── gateway.db-wal
├── gateway.db-shm
└── backups/
    ├── pre-migrate-v0-*.db    # 每次有待应用迁移时自动备份
    ├── pre-restore-v*.db      # 恢复前自动备份
    └── backup-*.db            # gateway backup 生成的备份
```

启动时 `serve` 会应用内嵌迁移。迁移前使用 `VACUUM INTO` 生成
`backups/pre-migrate-v<from>-<timestamp>.db`；每个迁移文件在独立事务中执行，
失败会回滚当前迁移并中止启动。迁移只前进、不自动回滚。

schema v2 新增连接版本和检查上下文，不改写 v1 的迁移文件。升级会清除没有版本证据的旧检查结果，
保留渠道、Key和模型映射。若需要降回仅支持v1的二进制，应先停服，用匹配版本程序恢复升级前备份，
再启动旧版本；不要直接让旧程序打开v2库，也不要手工删迁移记录。

从 Windows 迁移到 Linux/Docker：

1. 停止旧进程（SQLite WAL 需要干净关闭）。
2. 复制整个 `GATEWAY_DATA_DIR`（含 `backups/`）到目标机器。
3. 保持同一个 `GATEWAY_MASTER_KEY`；丢失主密钥将无法解密上游 Key。
4. 修正目录权限：容器以 UID/GID `10001` 运行。
5. 用新版本二进制的 `serve` 或 `gateway migrate` 触发升级。

## 5. Docker

镜像使用多阶段构建：Node 阶段执行 `npm ci` 与 `npm run build`，Go 阶段把
`internal/webassets/dist` 嵌入二进制。运行镜像基于 Alpine，非 root 用户
`10001:10001`，包含 `ca-certificates`，`/data` 为数据卷。

### 5.1 构建

```bash
docker build -t ai-router:local .
```

构建上下文由 `.dockerignore` 限制：不包含 `.git`、`reference/`、`docs/`、
本机数据目录、`web/node_modules` 与本机残留的 `internal/webassets/dist`。
管理端产物只来自构建阶段的 `npm --prefix web run build`。

### 5.2 运行

准备环境文件。放在仓库目录之外（例如用户配置目录），避免误提交；权限设为 `0600`：

```bash
mkdir -p ~/.config/ai-router
cat > ~/.config/ai-router/gateway.env <<'EOF'
GATEWAY_MASTER_KEY=...
GATEWAY_CLIENT_KEY=...
GATEWAY_ADMIN_PASSWORD=...
GATEWAY_COOKIE_SECURE=true
EOF
chmod 600 ~/.config/ai-router/gateway.env

docker volume create ai-router-data
docker run -d --name ai-router --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v ai-router-data:/data \
  --env-file ~/.config/ai-router/gateway.env \
  ai-router:local
```

镜像内已经设置：

- `GATEWAY_DATA_DIR=/data`
- `GATEWAY_LISTEN_ADDR=0.0.0.0:8080`
- `VOLUME /data`
- `EXPOSE 8080`
- 非 root `USER 10001:10001`

保持容器内监听 `0.0.0.0`，在宿主机用 `-p 127.0.0.1:8080:8080` 限制来源，
再由反向代理暴露 TLS。若把容器内地址改成 `127.0.0.1`，端口映射将无法访问。

使用宿主目录而不是命名卷时，目录必须对 UID 10001 可写：

```bash
sudo mkdir -p /srv/ai-router/data
sudo chown -R 10001:10001 /srv/ai-router/data
docker run ... -v /srv/ai-router/data:/data ...
```

不要使用 `--rm` 运行长期服务，否则忘记挂载卷时数据会随容器删除。

### 5.3 容器内运维

```bash
docker exec ai-router gateway backup
docker cp ai-router:/data/backups/<file>.db ./<file>.db
docker inspect --format '{{.State.Health.Status}}' ai-router
docker exec ai-router gateway version
```

## 6. 反向代理与 TLS

进程内不终止 TLS。管理端和 API 都应通过 HTTPS 反向代理访问，并且只把上游
绑定到回环地址。SSE 必须关闭代理缓冲。

### Nginx

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name gateway.example.com;

    ssl_certificate     /etc/letsencrypt/live/gateway.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/gateway.example.com/privkey.pem;

    client_max_body_size 32m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";

        # SSE：禁止缓冲与缓存，放宽读超时。
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

### Caddy

```caddyfile
gateway.example.com {
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
    }
}
```

TLS 场景设置 `GATEWAY_COOKIE_SECURE=true`。管理端响应默认 `no-store`；
反向代理不要对 `/admin` 或 `/admin/api` 做缓存。

## 7. 备份与恢复

### 7.1 手动备份

```bash
# 使用当前 GATEWAY_DATA_DIR，默认写入 <data>/backups/backup-<timestamp>.db
./bin/gateway backup

# 或指定目标文件
./bin/gateway backup /secure/location/gateway-$(date +%Y%m%d).db
```

备份使用 SQLite `VACUUM INTO`，是一致性快照。备份文件包含加密后的上游 Key
和请求元数据，同样按敏感数据保管。

### 7.2 恢复

恢复会完整替换数据库。**先停止服务**，并确认没有其他进程占用数据库：

```bash
# 本机
./bin/gateway restore /path/to/backup.db

# Docker（先停止长期容器）
docker stop -t 35 ai-router
docker run --rm \
  -v ai-router-data:/data \
  -v "$PWD":/backup \
  ai-router:local restore /backup/<file>.db
docker start ai-router
```

恢复步骤：

1. 只读打开备份，执行 `integrity_check`、`foreign_key_check`、核心表及迁移历史校验（版本、名称、checksum、历史缺口）；
2. 当前库存在时先生成 `backups/pre-restore-v<version>-<timestamp>.db`；
3. 对当前库执行 WAL checkpoint，复制备份到同目录临时文件并再次校验；
4. 清理旧库残留的 `gateway.db-wal` / `gateway.db-shm`，再替换主数据库。

预检失败不会覆盖当前数据库；checkpoint不是跨进程独占锁，整个恢复过程仍须停服。
不要在运行时直接替换数据库或删除WAL。备份兼容不代表密钥匹配，启动时的渠道解密检查仍会执行。

## 8. 迁移失败处理

- `serve` 启动时发现待应用迁移，会先生成 `backups/pre-migrate-v<from>-<timestamp>.db`。
- 单个迁移失败会在事务中回滚，进程以非零状态退出，不会开始监听。
- 失败后原数据库内容保持不变；修复方法：
  1. 保留失败日志与备份文件；
  2. 回退到迁移前二进制，执行 `./bin/gateway migrate` 确认 schema；
  3. 必要时用 `restore` 恢复 pre-migrate 备份；
  4. 不要手工删除 `schema_migrations` 记录来"跳过"失败。
- checksum 不匹配、数据库 schema 高于当前二进制、迁移文件乱序都会拒绝启动，
  这是防降级/防篡改保护，应换用匹配版本的二进制或从备份恢复。

## 9. 健康检查与优雅退出

- `GET /healthz`：进程存活即 200，不依赖数据库。
- `GET /readyz`：主密钥已加载、数据库可连接且 schema 与二进制一致时 200；
  否则 503，响应包含机器可读的 `reason`（如 `migration_pending`、
  `database_unavailable`、`shutting_down`）。
- 收到 `SIGTERM`/`SIGINT` 后立即进入 draining，`/readyz` 返回 503，然后等待
  进行中的请求，最长 30 秒；超时后取消剩余请求。
- 容器运行时使用 `docker stop -t 35 ai-router`，给进程留出完整退出时间。

## 10. 安全清单

- 镜像不包含主密钥、客户端 Key 或管理员密码；只用 `--env-file`/环境变量注入。
- `gateway.env` 权限 `0600`，不要提交到版本库。
- 主密钥稳定并离线备份；不要在容器重启时重新生成。
- 宿主机端口只绑定 `127.0.0.1`，公网只开放反向代理的 443。
- 备份文件与数据库同等敏感，复制和保存时限制权限。
- 管理端 session cookie 在生产环境启用 `Secure`。
- 不引入 Redis、消息队列或多实例共享状态；SQLite 是唯一存储。

## 11. 人工验收清单（发布前执行）

自动测试不会执行以下命令；每次发布前人工完成：

1. `docker build -t ai-router:local .` 成功。
2. 用命名卷和 `--env-file` 启动容器，`docker inspect` 确认：
   - `Config.User` 为 `10001:10001`；
   - `Config.Env` 只包含 `GATEWAY_DATA_DIR=/data`、`GATEWAY_LISTEN_ADDR=0.0.0.0:8080` 等非秘密值；
   - `Config.ExposedPorts` 包含 `8080/tcp`；
   - `Mounts` 包含 `/data`。
3. `curl -fsS http://127.0.0.1:8080/healthz` 返回 200；迁移完成后 `/readyz` 返回 200。
4. `curl -fsS http://127.0.0.1:8080/admin` 返回的 HTML 引用 `/assets/index-<hash>.js`，
   且该 JS 返回 200。
5. 停止并重新创建容器后，管理员登录、渠道配置与请求日志仍在（卷持久化）。
6. `docker stop -t 35` 后进程在 30 秒内退出；重启后 `/readyz` 恢复 200。
7. 在数据目录生成备份，删除/重建容器后用 `restore` 恢复成功。
8. 通过反向代理验证 SSE 不被缓冲，且 `GATEWAY_COOKIE_SECURE=true` 时登录正常。
