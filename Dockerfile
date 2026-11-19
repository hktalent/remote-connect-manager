# 构建阶段：使用 vendor 依赖，构建过程无需联网
FROM golang:latest AS builder

WORKDIR /app
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY main.go ./
COPY desktop.go ./
COPY internal ./internal
COPY web ./web
COPY payloads ./payloads
COPY README.md ./
RUN CGO_ENABLED=0 GOFLAGS=-mod=vendor go build -o remote-connect-manager .

# 运行阶段
FROM debian:bookworm-slim

WORKDIR /app

COPY --from=builder /app/remote-connect-manager /app/remote-connect-manager
COPY --from=builder /app/web /app/web
COPY --from=builder /app/payloads /app/payloads
COPY start.sh /app/start.sh
RUN chmod +x /app/start.sh /app/remote-connect-manager \
    && mkdir -p /app/data/screenshots /app/data/keys

EXPOSE 8080

CMD ["/app/start.sh"]
