# syntax=docker/dockerfile:1
#
# 多阶段构建：Node 构建管理端 SPA，Go 构建嵌入静态资源的单二进制。
# 运行镜像不包含 Node、Go 工具链或任何秘密；秘密只能通过运行时环境注入。

# ---- 管理端构建 ---------------------------------------------------------
FROM node:24-alpine AS web

WORKDIR /src

# 先复制清单并安装依赖，利用 Docker 层缓存；npm ci 严格遵循 package-lock。
COPY web/package.json web/package-lock.json ./web/
RUN npm --prefix web ci

COPY web ./web
# Vite outDir=../internal/webassets/dist，产物落在 /src/internal/webassets/dist。
RUN npm --prefix web run build

# ---- Go 构建 -----------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

# internal/webassets/dist 在 .dockerignore 中排除；构建产物只来自 web 阶段，
# 避免把本机残留的占位页或旧哈希打进镜像。
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web /src/internal/webassets/dist ./internal/webassets/dist

RUN go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

# ---- 运行时 ------------------------------------------------------------
FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 gateway \
    && adduser -S -D -H -u 10001 -G gateway gateway \
    && mkdir -p /data \
    && chown -R 10001:10001 /data

WORKDIR /app

COPY --from=build /out/gateway /usr/local/bin/gateway

# 容器内监听所有网卡；宿主端口应只绑定回环地址，再由 TLS 反向代理暴露。
ENV GATEWAY_DATA_DIR=/data
ENV GATEWAY_LISTEN_ADDR=0.0.0.0:8080

VOLUME /data
EXPOSE 8080

USER 10001:10001

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/gateway"]
CMD ["serve"]
