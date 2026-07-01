FROM golang:1.24 AS builder

WORKDIR /app

# 使用国内 Go module 代理，解决 proxy.golang.org 被墙问题
ENV GOPROXY=https://goproxy.cn,https://goproxy.io,direct

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/api ./cmd/api/main.go

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/*

ENV TZ=Asia/Shanghai

COPY --from=builder /out/api /app/api
COPY config /app/config
COPY docs /app/docs

RUN mkdir -p /app/logs

EXPOSE 8080

CMD ["/app/api"]
