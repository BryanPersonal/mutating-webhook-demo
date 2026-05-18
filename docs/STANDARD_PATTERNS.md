# Mutating Webhook Demo — 标准开发范式学习指南

本文从本仓库提炼 **Kubernetes Mutating Webhook + controller-runtime** 场景下常见、可复用的工程范式，并标明哪些是「业界标准做法」、哪些是「教学/POC 简化」。配合 [ARCHITECTURE.md](./ARCHITECTURE.md)（架构图与模块说明）、[DESIGN.md](./DESIGN.md)（原理与代码走读）和 [README.md](../README.md)（部署与验证）阅读。

---

## 1. 如何阅读本仓库

| 层次 | 文件 | 标准范式 |
|------|------|----------|
| 进程组装 | `main.go` | Manager 生命周期、探针与 Webhook 端口分离、Runnable 注册顺序 |
| 准入业务 | `webhook.go` | CustomDefaulter、路径与集群配置一致、MWC 安装/更新 |
| TLS | `certs.go` | 服务端证书 SAN、caBundle 闭环 |
| 集群清单 | `deploy/` | Kustomize、RBAC、自举排除、环境变量注入 |
| 工程化 | `Makefile` / `Dockerfile` | 本地集群迭代、多阶段镜像、vendor 离线构建 |
| 对照模板 | `mutating-webhook-config.yaml` | GitOps / 手工 apply 时的配置真源 |

---

## 2. 项目结构：按职责拆分（标准）

生产级 Webhook 项目通常不会把所有逻辑塞进 `main`，本仓库采用 **薄入口 + 领域文件**：

```
main.go      → 只负责：日志、证书目录、NewManager、健康检查、注册 Handler、可选 Runnable、Start
webhook.go   → 只负责：Register 路径、Default 业务、MutatingWebhookConfiguration 构造与安装
certs.go     → 只负责：CertDir、SAN、PEM 读写
deploy/      → 只负责：运行时身份、网络、RBAC、环境开关
```

**学习要点**：改准入规则时主要动 `webhook.go`；改 TLS/轮换时动 `certs.go`；改「谁有权写 MWC」时动 `deploy/rbac.yaml`。这样 Code Review 和排障路径清晰。

---

## 3. controller-runtime 标准范式

### 3.1 `runtime.Scheme` 注册（必做）

在 `init()` 中向 Scheme 注册 API 类型，供 admission 解码 `AdmissionReview` 中的对象：

```go
var scheme = runtime.NewScheme()

func init() {
    utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}
```

**标准原因**：未注册的类型无法正确反序列化为 `*corev1.Pod` 等强类型。

### 3.2 Manager + WebhookServer（推荐栈）

```go
mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
    Scheme: scheme,
    WebhookServer: webhook.NewServer(webhook.Options{
        Port:    9443,
        CertDir: certDir,
    }),
    HealthProbeBindAddress: ":8081",
})
```

| 约定 | 本仓库 | 说明 |
|------|--------|------|
| Webhook HTTPS | `9443` | 社区常见；与 apiserver 默认无冲突 |
| 健康检查 | `8081` | **与 admission 端口分离**，避免探针打到 TLS 端口 |
| 集群配置 | `GetConfigOrDie()` | Pod 内即 in-cluster config |

### 3.3 `WithCustomDefaulter` + `Register`（Mutating 标准写法）

```go
wh := admission.WithCustomDefaulter(scheme, &corev1.Pod{}, &podMutator{}).WithRecoverPanic(true)
mgr.GetWebhookServer().Register(mutatePodPath, wh)
```

**框架替你完成（标准收益）**：

- 解析 `AdmissionReview` / 构造 `AdmissionResponse`
- 将请求体解码为 `*corev1.Pod`
- 根据 `Default` 修改生成 JSON Patch
- `WithRecoverPanic(true)` 避免 handler panic 拖垮进程

**业务只需实现**：`Default(ctx, obj)` 内对对象做原地修改（见 `webhook.go`）。

### 3.4 健康检查注册（标准）

```go
mgr.AddHealthzCheck("healthz", healthz.Ping)
```

Deployment 应配置 `livenessProbe` / `readinessProbe` 指向 `:8081`（生产清单中可补全；本 demo 以端口分离为主）。

### 3.5 启动顺序：cache 与 Runnable（重要坑位）

| 阶段 | 可做 | 不可做 |
|------|------|--------|
| `mgr.Start` **之前** | `addPodMutator`（仅 `Register`） | `mgr.GetClient().Get/Create`（cache 未启动） |
| `mgr.Start` **之后**（Runnable 内） | `installMutatingConfig` 用 client 写集群 | — |

本仓库用环境变量 `INSTALL_WEBHOOK_CONFIG=1` 控制是否在 Runnable 中安装 `MutatingWebhookConfiguration`：

```go
mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
    return installMutatingConfig(ctx, mgr, caPEM)
}))
```

**标准结论**：凡依赖 **controller-runtime cached client** 的集群写操作，应放在 **Runnable** 或 **Reconciler** 里，而不是 `main` 里 `Start` 之前。

---

## 4. Kubernetes Admission Webhook 标准范式

### 4.1 两段式模型（必须同时成立）

1. **集群侧**：`MutatingWebhookConfiguration` — rules、Service、path、port、**caBundle**、`failurePolicy` 等。
2. **进程侧**：`WebhookServer.Register(path, handler)` — **path 与 MWC 中 `clientConfig.service.path` 完全一致**。

本仓库常量 `mutatePodPath = "/mutate-v1-pod"` 在两处共用，这是 **标准不变量**。

### 4.2 用 Go 类型构造 MWC（代码即配置）

`mutatingConfig()` 返回 `*admissionregistrationv1.MutatingWebhookConfiguration`，与 `mutating-webhook-config.yaml` 同构。

**标准实践**：

- **开发/自举**：进程内 Create/Update + 动态写入 `caBundle`（本仓库 `INSTALL_WEBHOOK_CONFIG=1`）。
- **生产/GitOps**：Helm/Kustomize/Operator 管理 MWC；进程 **只** 提供 HTTPS Handler；`caBundle` 由 cert-manager 或 CI 注入。

两种路径二选一或组合，但 **rules/path/port/selector 应单一真源**，避免 YAML 与代码漂移。

### 4.3 安装逻辑：Create 或 Update（标准幂等）

```go
err := cli.Get(ctx, client.ObjectKey{Name: cfg.Name}, existing)
if apierrors.IsNotFound(err) {
    return cli.Create(ctx, cfg)
}
existing.Webhooks = cfg.Webhooks
return cli.Update(ctx, existing)
```

**学习要点**：Pod 重启、证书轮换后必须能 **更新 `caBundle`**，否则 apiserver TLS 校验失败。这是比「只 Create 一次」更贴近生产的写法。

### 4.4 准入策略字段（生产常用默认值）

| 字段 | 本仓库值 | 标准含义 |
|------|----------|----------|
| `failurePolicy` | `Fail` | Webhook 不可用则拒绝匹配操作（强一致） |
| `sideEffects` | `NoneOnDryRun` | 支持 dry-run，符合 apiserver 约束 |
| `matchPolicy` | `Exact` | 精确匹配 GVK |
| `timeoutSeconds` | `10` | 防止 apiserver 长时间阻塞 |
| `admissionReviewVersions` | `["v1"]` | 使用稳定 API 版本 |

调试用可改为 `Ignore`，但生产 Mutating 通常慎用。

### 4.5 避免 Webhook 调用自身（标准排障范式）

**问题**：Webhook Deployment 所在 namespace 创建 Pod 时，若仍匹配 MWC，apiserver 会再调自己 → 启动/滚动时 TLS 或死锁。

**标准解法（本仓库采用）**：

- `deploy/namespace.yaml`：`mutating-webhook-demo.io/exclude: "true"`
- `mutatingConfig()`：`namespaceSelector` + `NotIn` 排除该 label

其它常见做法：Webhook 部署到 **独立 namespace** 且 rules 不命中；或使用 `objectSelector` 排除带特定 label 的 Pod。

---

## 5. TLS 与证书标准范式

### 5.1 apiserver 是 TLS 客户端

- Webhook 提供 **服务端证书**（`tls.crt` / `tls.key`，目录由 `CertDir` 约定）。
- MWC 的 **`caBundle`** 必须是 apiserver 信任的 CA（自签时即证书 PEM 本身）。

### 5.2 SAN 必须覆盖 Service DNS（Go 1.15+）

`certTemplate` 中 `DNSNames` 包含：

- `<service>.<namespace>.svc`
- `<service>.<namespace>.svc.cluster.local`
- `localhost`（本地调试）

**仅填 CN 不够** — 这是 Webhook 集成里最高频的 TLS 错误之一。

### 5.3 证书目录与启动顺序

1. `ensureCertDir` **先于** `NewManager`（保证 WebhookServer 启动时磁盘上已有 key/cert）。
2. 返回的 `caPEM` 供后续写入 MWC。

### 5.4 演示 vs 生产

| 做法 | 本仓库 | 生产标准 |
|------|--------|----------|
| 证书来源 | Pod 内 `emptyDir` 自签 | cert-manager、certificate-controller、云 KMS |
| 轮换 | 新 Pod 生成新证 + Update MWC | 固定 Secret 挂载 + 滚动 + 自动更新 caBundle |
| 私钥权限 | `tls.key` 0600 | Secret + 只读挂载 |

---

## 6. 部署与运维标准范式

### 6.1 Kustomize 一键部署

```bash
kubectl apply -k deploy/
```

`kustomization.yaml` 聚合 namespace / rbac / service / deployment — **标准本地与 CI 入口**。

### 6.2 RBAC：最小权限面向任务

ClusterRole 仅授予 `mutatingwebhookconfigurations` 的 get/list/watch/create/update/patch — **仅为「自举安装 MWC」服务**，不是 Webhook 处理 admission 所必需（admission 由 apiserver 回调，不经过 client-go 写 Pod）。

### 6.3 Downward API 注入 namespace

```yaml
- name: NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
```

**标准原因**：`mutatingConfig()` 里 `ServiceReference.Namespace` 与 Pod 实际所在 namespace 一致，避免硬编码漂移。

### 6.4 功能开关用环境变量

`INSTALL_WEBHOOK_CONFIG=1` — **标准「同一镜像、不同部署模式」**：  
集群内自举 vs 仅 Handler（由 GitOps apply MWC）。

### 6.5 Makefile 驱动的开发闭环（标准本地工作流）

| 命令 | 范式 |
|------|------|
| `make up` | 集群 → 镜像 → 导入 → deploy → wait Ready |
| `make dev` | 改代码 → 重建镜像 → rollout restart |
| `make verify` | 端到端断言（创建 Pod → 检查 label） |
| `make down` | 卸载 + 删除 MWC，避免脏集群 |

这是 **「构建 → 部署 → 自动化验证」** 三件套，可平移到 CI Pipeline。

### 6.6 Docker 多阶段 + vendor（标准供应链）

- **builder**：`go build -mod=vendor`，构建不依赖外网拉模块。
- **runtime**：最小运行时镜像 + `ca-certificates`。
- **平台**：`DOCKER_PLATFORM=linux/amd64` 避免本地 arm 构建与集群架构不一致。

依赖变更时在本机执行 `go mod vendor` 再构建 — **企业内网/代理环境常见做法**。

---

## 7. 测试与验证范式

### 7.1 手工/E2E（本仓库）

`make verify`：在 **非排除** 的 namespace 创建 Pod，检查 `mutated-by=my-webhook`。

### 7.2 单元/集成（标准扩展，仓库未实现）

| 层级 | 工具 | 测什么 |
|------|------|--------|
| Handler 逻辑 | `go test` + fake `runtime.Object` | `podMutator.Default` 是否写入 label |
| 全链路 | **envtest** | 启动 apiserver + 注入 MWC + 起 Manager，Create Pod 断言 |

README 已提示 envtest 方向 — **生产项目应补 `*_test.go`**，本 demo 以 Makefile E2E 为主。

---

## 8. 与更大项目对齐的「对照学习」范式

注释中对照 **persephone** 的映射关系，属于 **「用已知大项目理解小 demo」** 的学习方式：

| persephone | 本仓库 |
|------------|--------|
| `cmd/persephone-webhook/main.go` | `main.go` |
| `internal/webhook/add.go` | `webhook.go` 的 `addPodMutator` |
| `GetMutatingWebhookConfiguration()` | `mutatingConfig()` |
| Gardener 证书管理 | `certs.go` 自签（简化） |

迁移到生产时：**保留两段式模型与 Register/path 不变量**；替换证书与 MWC 安装方式即可。

---

## 9. 检查清单：写新 Webhook 时按顺序自检

1. [ ] `Scheme` 已注册目标资源类型  
2. [ ] `Register` 的 path 与 MWC `clientConfig.service.path` 一致  
3. [ ] Service 名、namespace、端口与 MWC 一致  
4. [ ] 证书 SAN 覆盖 `service.namespace.svc` 与 `.svc.cluster.local`  
5. [ ] `caBundle` 与当前服务端证书匹配（轮换后能 Update）  
6. [ ] 已处理「Webhook 调自己」（namespace/object selector 或独立 namespace）  
7. [ ] `failurePolicy` / `sideEffects` 按环境选定  
8. [ ] 依赖 client cache 的逻辑在 Runnable/Reconciler 内，不在 `Start` 前  
9. [ ] 健康检查端口 ≠ Webhook TLS 端口  
10. [ ] RBAC 仅授予写 MWC 所需权限；生产考虑 GitOps 管理 MWC  

---

## 10. 本仓库「标准」与「仅 Demo」对照

| 范式 | 是否业界标准 | 本仓库实现 |
|------|----------------|------------|
| controller-runtime Manager + WebhookServer | ✅ | 是 |
| CustomDefaulter 写 Mutating 逻辑 | ✅ | 是 |
| path / MWC 双端一致 | ✅ | 是 |
| Runnable 内安装集群配置 | ✅ | 是 |
| namespace 排除自调用 | ✅ | 是 |
| 探针与 Webhook 端口分离 | ✅ | 是（清单可再补 probe） |
| failurePolicy Fail + NoneOnDryRun | ✅ | 是 |
| 进程内自签 + emptyDir | ⚠️ 教学用 | 是；生产换 cert-manager |
| INSTALL_WEBHOOK_CONFIG 自举 MWC | ⚠️ 可选 | 是；生产常用 GitOps |
| vendor + 内网基础镜像 | ✅ 企业常见 | 是 |
| envtest 单测 | ✅ 推荐 | 未包含，可自行扩展 |

---

## 11. 推荐阅读顺序

1. 本文（范式总览）  
2. [DESIGN.md](./DESIGN.md) §3–§8（代码与原理）  
3. [README.md](../README.md) `make up` / `make verify` 实操  
4. 对照阅读 `webhook.go` 与 `mutating-webhook-config.yaml`  

若你正在把该模式迁入生产（如 persephone），优先固化 **§4 两段式**、**§5 TLS**、**§3.5 Runnable 顺序** 与 **§4.5 自举排除**，再替换证书与 MWC 交付方式。
