# syntax=docker/dockerfile:1

# 第一阶段只负责编译 Go 服务。依赖和源码分开复制，使 go.mod/go.sum
# 没变化时可以复用模块下载缓存。
FROM golang:1.25.6-bookworm AS go-builder

WORKDIR /src/backend

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/rag-server \
    ./cmd/server

# 第二阶段是实际运行镜像。它不包含 Go 编译器，但保留 Python 3.11，
# 因为当前 Go Worker 会通过受控子进程调用 rag_ai 处理 PDF。
FROM python:3.11.16-slim-bookworm AS runtime

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    GIN_MODE=release \
    APP_ROOT=/app \
    STORAGE_ROOT=storage \
    PYTHON_EXECUTABLE=python3 \
    PYTHON_SOURCE_ROOT=ai/src \
    APP_PORT=8080

WORKDIR /app

COPY ai/pyproject.toml /app/ai/pyproject.toml
COPY ai/src/ /app/ai/src/
RUN python3 -m pip install /app/ai

COPY --from=go-builder /out/rag-server /usr/local/bin/rag-server

# 生产进程不使用 root。10001 是容器内固定 UID；Linux 宿主机绑定
# storage 目录时，需要保证该 UID 对目录拥有写权限。
RUN useradd --create-home --uid 10001 appuser \
    && mkdir -p /app/storage \
    && chown -R appuser:appuser /app/storage

USER appuser

EXPOSE 8080

# Python 已经是文档处理运行依赖，因此直接使用标准库探测现有 /health，
# 不额外安装 curl 或 wget。
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=5 \
    CMD ["python3", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/health', timeout=2).read()"]

ENTRYPOINT ["rag-server"]
