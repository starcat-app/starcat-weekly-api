# ===========================================
# Stage 1: 构建阶段
# ===========================================
# 与 starcat-sharing-api / starcat-trending-api 保持一致的多阶段构建
# Go 版本由项目 go.mod 决定 (1.25.0)
FROM golang:1.25-alpine AS builder

# 设置工作目录
WORKDIR /app

# 先复制依赖文件, 利用 Docker 缓存
# 当前项目外部依赖: robfig/cron, goldmark, modernc.org/sqlite (含若干 indirect)
COPY go.mod go.sum* ./
RUN go mod download

# 复制源码并编译
COPY . .
# CGO_ENABLED=0 编译为静态二进制
#   关键: modernc.org/sqlite 是纯 Go 实现 (把 SQLite C 翻译成 Go),
#   不需要 CGO, 但编译期会拉很多 Go 翻译代码, build 时间较长 (1-2 分钟)
# -ldflags="-w -s" 去除调试信息, 减小二进制体积
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s" \
    -o /app/bin/server \
    ./cmd/server/

# ===========================================
# Stage 2: 运行阶段
# ===========================================
# 使用 alpine 基础镜像保持体积小巧
# 与 sharing / trending 保持一致: alpine 3.21
FROM alpine:3.21

# 安装 git (fetcher 需要 git clone / pull 拉取 ruanyf/weekly)
#   + CA 证书 (HTTPS 请求 GitHub API)
#   + tzdata (cron / 日志需要正确时区)
RUN apk --no-cache add ca-certificates tzdata git

# 设置时区
ENV TZ=UTC

# 创建非 root 用户运行服务 (安全最佳实践)
RUN addgroup -S app && adduser -S app -G app

# 工作目录固定在 /app
# - /app/server   : 编译产物
# 数据文件 / 周刊源码缓存由环境变量 (STORE_FILE / .weekly-repo) 指向挂载卷 /data,
# 不在镜像中
WORKDIR /app

# 从 builder 阶段复制编译产物
COPY --from=builder /app/bin/server /app/server

# 切换到非 root 用户
USER app

# 暴露端口 (与 main.go 默认 5003 一致)
EXPOSE 5003

# 健康检查 (与 main.go /healthz 端点对齐: 30s/5s/10s grace)
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://localhost:5003/healthz || exit 1

# 启动服务
ENTRYPOINT ["/app/server"]
