# WorkTool-Open 服务端镜像（W-M4 / G-5 compose 交付；docs/02-ARCHITECTURE.md §4、
# docs/06 SERVER-TECH-DECISION：Go 单二进制 + 免 CGO）。
#
# 构建上下文 = 仓库根（compose 里 build.context: ".."），只消费 server/。
#   docker build -t worktool-open-server:latest -f infra/server.Dockerfile .
#
# 设计约束：
#   - 多阶段构建，最终镜像不含工具链与源码（alpine + 单二进制 + ca-certificates）。
#   - Go 版本与 server/go.mod 的 go 指令一致（1.23，与 server.yml CI 钉版相同）。
#   - 非 root 运行（uid 1000）；SQLite 数据全部写 /data（compose 挂 named volume）。
#   - SR-09 由二进制自身保证：容器内绑定 0.0.0.0 属非 loopback，未启用 TLS 时
#     进程启动即拒绝——compose 默认形态即 TLS（证书挂载见 compose.yaml）。

FROM golang:1.23-alpine AS build
WORKDIR /src
# GOPROXY 仅是传输层，可按网络环境覆盖（如内网/受限网络用镜像）——依赖完整性
# 由 go.sum 哈希校验保证（server.yml CI 亦跑 go mod verify），换 proxy 不降门槛。
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
# 先拷 go.mod/go.sum 单独下载依赖（层缓存：源码改动不重下依赖）
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/worktool-open-server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 1000 wt && adduser -D -u 1000 -G wt wt \
    && mkdir -p /data && chown -R wt:wt /data
COPY --from=build /out/worktool-open-server /usr/local/bin/worktool-open-server
USER wt
WORKDIR /data
EXPOSE 8443
ENTRYPOINT ["worktool-open-server"]
# 默认形态：容器内对外绑定 + TLS（SR-09）；证书与库文件由 compose 挂载注入
CMD ["-listen", "0.0.0.0:8443", "-tls-cert", "/certs/server.crt", "-tls-key", "/certs/server.key", "-db", "/data/worktool.db"]
