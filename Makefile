# MutatingWebhook Demo - k3d 本地集群快速调试
# 用法: make up && make verify  或  make dev

CLUSTER_NAME ?= mycluster
IMAGE_NAME   ?= mutating-webhook-demo:latest
NAMESPACE    ?= mutating-webhook-demo

.PHONY: help cluster-create cluster-delete ensure-cluster \
	build image image-import deploy undeploy up down \
	logs restart verify clean

help:
	@echo "k3d 本地调试常用命令:"
	@echo "  make up        - 确保集群存在 → 构建镜像 → 导入 k3d → 部署（一键拉起）"
	@echo "  make down      - 卸载部署并删除 MutatingWebhookConfiguration"
	@echo "  make dev       - 同 up，适合改代码后重新构建并重启 Pod"
	@echo "  make logs      - 查看 Webhook Pod 日志"
	@echo "  make verify    - 创建测试 Pod 并检查 label"
	@echo ""
	@echo "分步执行:"
	@echo "  make ensure-cluster - 若集群不存在则创建"
	@echo "  make build          - 本地 go build"
	@echo "  make image          - 构建 Docker 镜像"
	@echo "  make image-import   - 将镜像导入 k3d（依赖 image）"
	@echo "  make deploy        - kubectl apply -k deploy/"
	@echo "  make restart       - 重启 Deployment（改代码后 make image image-import restart）"
	@echo "  make undeploy      - 删除 deploy/ 资源"
	@echo "  make cluster-delete - 删除 k3d 集群"
	@echo ""
	@echo "变量: CLUSTER_NAME=$(CLUSTER_NAME)  IMAGE_NAME=$(IMAGE_NAME)  NAMESPACE=$(NAMESPACE)"

# 若集群不存在则创建
ensure-cluster:
	@k3d cluster list 2>/dev/null | grep -q $(CLUSTER_NAME) || k3d cluster create $(CLUSTER_NAME)

cluster-create:
	k3d cluster create $(CLUSTER_NAME)

cluster-delete:
	k3d cluster delete $(CLUSTER_NAME)

# 本地编译（不依赖 Docker）
build:
	go build -o mutating-webhook-demo .

# 构建 Docker 镜像（显式 linux/amd64 与 SAP 基础镜像一致，避免 arm64 平台警告）
# 若 proxy.golang.org 超时，可: make image GOPROXY=direct 或使用内部 Go 代理
DOCKER_PLATFORM ?= linux/amd64
GOPROXY_ARG     ?=
image:
	docker build --platform $(DOCKER_PLATFORM) $(if $(GOPROXY_ARG),--build-arg GOPROXY=$(GOPROXY_ARG),) -t $(IMAGE_NAME) .

# 将镜像导入 k3d，便于集群内使用
image-import: image
	k3d image import $(IMAGE_NAME) -c $(CLUSTER_NAME)

# 部署 Webhook（Namespace + RBAC + Deployment + Service）
deploy:
	kubectl apply -k deploy/

# 卸载部署并删除 MutatingWebhookConfiguration
undeploy:
	kubectl delete -k deploy/ --ignore-not-found
	kubectl delete mutatingwebhookconfiguration mutate-pod-demo --ignore-not-found

# 一键拉起：确保集群 → 构建并导入镜像 → 部署
up: ensure-cluster image-import deploy
	@echo "等待 Pod 就绪..."
	@kubectl -n $(NAMESPACE) wait --for=condition=ready pod -l app=mutating-webhook-demo --timeout=120s
	@echo "up 完成。可执行 make verify 验证 Webhook。"

# 改代码后快速迭代：重新构建镜像、导入、重启 Pod
dev: image-import
	kubectl -n $(NAMESPACE) rollout restart deployment mutating-webhook-demo
	@echo "等待 Pod 就绪..."
	@kubectl -n $(NAMESPACE) rollout status deployment mutating-webhook-demo --timeout=120s
	@echo "dev 完成。可执行 make logs 看日志、make verify 验证。"

down: undeploy

# 查看 Webhook Pod 日志（便于调试）
logs:
	kubectl -n $(NAMESPACE) logs -f deployment/mutating-webhook-demo

# 重启 Deployment（配合 make image image-import 使用）
restart:
	kubectl -n $(NAMESPACE) rollout restart deployment mutating-webhook-demo
	kubectl -n $(NAMESPACE) rollout status deployment mutating-webhook-demo --timeout=120s

# 创建测试 Pod 并检查是否被注入 label
verify:
	@kubectl delete pod verify-nginx --ignore-not-found --timeout=5s 2>/dev/null || true
	@echo "创建 Pod verify-nginx ..."
	@kubectl run verify-nginx --image=nginx --restart=Never
	@echo "等待 Pod 就绪..."
	@kubectl wait --for=condition=ready pod verify-nginx --timeout=60s
	@echo "检查 label mutated-by:"
	@kubectl get pod verify-nginx -o jsonpath='{.metadata.labels.mutated-by}' && echo ""
	@v=$$(kubectl get pod verify-nginx -o jsonpath='{.metadata.labels.mutated-by}'); \
	if [ "$$v" = "my-webhook" ]; then echo "OK: Webhook 已注入 label"; else echo "FAIL: 期望 my-webhook，得到 $$v"; exit 1; fi
	@kubectl delete pod verify-nginx --ignore-not-found 2>/dev/null || true

# 完全清理：卸载 + 删除集群
clean: undeploy
	k3d cluster delete $(CLUSTER_NAME) 2>/dev/null || true
	@echo "clean 完成。"
