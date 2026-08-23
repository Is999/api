# 构建阶段固定使用项目声明的 Go 工具链。
FROM golang:1.26.6 AS build

ARG VERSION=dev

# 先复制依赖清单以复用模块下载层。
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# 源码变更只使后续编译层失效。
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildVersion=${VERSION}" -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildVersion=${VERSION}" -o /out/api-migrate ./cmd/migrate

# 运行阶段只保留证书、时区和健康探针依赖。
FROM debian:bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl tzdata \
  && rm -rf /var/lib/apt/lists/*

# 非 root 用户只拥有运行目录和可写数据目录。
WORKDIR /app
RUN groupadd --system app && useradd --system --gid app --home-dir /app --no-create-home app \
  && mkdir -p /app/etc /app/logs /app/data \
  && chown -R app:app /app
COPY --from=build /out/api /usr/local/bin/api
COPY --from=build /out/api-migrate /usr/local/bin/api-migrate
COPY etc/config.sample.yaml /app/etc/config.sample.yaml

# 镜像只声明公网端口，内网端口由部署配置显式发布。
EXPOSE 8890
USER app

# 存活探针只检查进程 HTTP 能力，不探测外部依赖。
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8890/api/live || exit 1

# 配置文件由部署系统挂载到固定路径。
ENTRYPOINT ["/usr/local/bin/api"]
CMD ["-f", "/app/etc/config.yaml"]
