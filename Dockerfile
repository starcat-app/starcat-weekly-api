# starcat-weekly-api Dockerfile
# 多阶段构建，参考 starcat-sharing-api 风格

# ── Stage 1: 编译 ────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /app

# 利用 Docker 缓存：先复制依赖文件
COPY go.mod go.sum* ./
RUN go mod download

# 复制源码并编译
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s" \
    -o /app/bin/server \
    ./cmd/server/

# ── Stage 2: 运行 ────────────────────────────────────────
FROM alpine:3.21

# 安装 git（fetcher 需要 git clone/pull）+ CA 证书
RUN apk --no-cache add ca-certificates tzdata git

ENV TZ=UTC

RUN addgroup -S app && adduser -S app -G app

WORKDIR /app

COPY --from=builder /app/bin/server /app/server

USER app

EXPOSE 5001

# 健康检查（与 fly.toml 保持一致）
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://localhost:5001/healthz || exit 1

ENTRYPOINT ["/app/server"]
