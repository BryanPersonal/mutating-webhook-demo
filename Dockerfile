# 多阶段构建：使用 vendor，构建时不再访问外网（无 go mod download）
# 首次或依赖变更后在本机执行: go mod vendor（需能访问 Go 模块源）
FROM suse.int.repositories.cloud.sap/bci/golang:1.23 AS builder
WORKDIR /app
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o mutating-webhook-demo .

# FIXME: Security Fixes with renovate 
FROM cia-docker-live.int.repositories.cloud.sap/baseimage-alpine:3.18.2
RUN apk --no-cache add ca-certificates
WORKDIR /
COPY --from=builder /app/mutating-webhook-demo .
ENTRYPOINT ["/mutating-webhook-demo"]
