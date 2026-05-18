# MutatingWebhook 最小可运行示例

对 Pod 的 **Create / Update** 自动添加 label `mutated-by=my-webhook`，用于理解 Kubernetes MutatingWebhook 与 controller-runtime 的用法。对应 [persephone](https://github.wdf.sap.corp/sap-cloud-infrastructure/persephone) 中 `internal/webhook/add.go` 的设计。

**设计要点（代码解读、原理、启动顺序、TLS、自举）**：见 [docs/DESIGN.md](docs/DESIGN.md)。

**代码剖析图与 Go 模块说明（架构图、调用关系、依赖表）**：见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。

**标准开发范式（工程结构、controller-runtime、准入/TLS、部署与检查清单）**：见 [docs/STANDARD_PATTERNS.md](docs/STANDARD_PATTERNS.md)。

## 代码树（本仓库源文件）

```
mutating-webhook-demo/
├── main.go                         # Manager / WebhookServer / 探针 / 可选安装 MutatingWebhookConfiguration
├── webhook.go                      # Register 路径、podMutator、mutatingConfig、installMutatingConfig
├── certs.go                        # CertDir 自签证书，SAN 含 Service DNS
├── mutating-webhook-config.yaml    # 与代码等价的集群配置模板（手动 apply 需自行填 caBundle）
├── Makefile                        # k3d：up / dev / verify / logs 等
├── Dockerfile
├── go.mod / go.sum
└── deploy/                         # kubectl apply -k deploy/
    ├── kustomization.yaml
    ├── namespace.yaml              # 打 mutating-webhook-demo.io/exclude，避免 Webhook 调自己
    ├── rbac.yaml
    ├── service.yaml
    └── deployment.yaml
```

（依赖源码在 `vendor/`，构建镜像时使用 `-mod=vendor`。）

## 结构说明

| 文件 | 对应 persephone | 作用 |
|------|-----------------|------|
| `main.go` | `cmd/persephone-webhook/main.go` | 创建 Manager、WebhookServer（含 CertDir），证书生成后可选安装 MutatingWebhookConfiguration |
| `webhook.go` | `internal/webhook/add.go` + shootadmission | `addPodMutator`、`mutatingConfig()`、`installMutatingConfig(ctx, mgr, caPEM)`（写入 caBundle） |
| `certs.go` | （persephone 用 Gardener 证书管理） | 启动时在 CertDir 生成自签证书（tls.crt/tls.key），供 API Server 通过 caBundle 信任 |
| `deploy/` | - | Namespace、RBAC、Deployment、Service，用于在 k3d/任意集群中一键部署 |

**两段式理解**：

1. **集群配置**（`MutatingWebhookConfiguration`）：告诉 API Server 对哪些资源（此处为 `pods` 的 Create/Update）调哪个 Service 和路径，并需设置 **caBundle** 以信任 Webhook 的 TLS。
2. **进程内 Handler**：在 WebhookServer 上 `Register(路径, webhook)`，路径与配置中的 `ClientConfig` 一致；`CustomDefaulter.Default(ctx, obj)` 中修改对象并返回。

## 前置条件

- Go 1.22+
- 可用的 Kubernetes 集群（或 envtest、kind、minikube、**k3d** 等），且 `KUBECONFIG` 已配置
- 在 k3d 上部署时还需：Docker、k3d CLI

---

## 在 k3d 上部署并做 Mutating Webhook 测试（推荐）

**快速调试**：项目根目录提供 **Makefile**，可一键拉起或迭代开发：

```bash
make up      # 创建集群（若不存在）→ 构建镜像 → 导入 k3d → 部署
make verify  # 创建测试 Pod 并检查 label
make dev     # 改代码后：重新构建镜像、导入、重启 Pod
make logs    # 查看 Webhook 日志
make down    # 卸载部署
make help    # 查看所有命令
```

以下为等价的手动步骤（无需 Make 时使用）。

### 1. 创建 k3d 集群

```bash
k3d cluster create mycluster
kubectl cluster-info
```

### 2. 构建镜像并导入 k3d

在项目根目录（`mutating-webhook-demo/`）执行：

```bash
# 若无 go.sum，先执行：go mod tidy
docker build -t mutating-webhook-demo:latest .
k3d image import mutating-webhook-demo:latest -c mycluster
```

### 3. 部署 Webhook（Namespace + RBAC + Deployment + Service）

```bash
# 一键部署（推荐）
kubectl apply -k deploy/

# 或逐文件部署
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/service.yaml
kubectl apply -f deploy/deployment.yaml
```

Deployment 中已设置 `INSTALL_WEBHOOK_CONFIG=1` 和 `NAMESPACE=mutating-webhook-demo`，Pod 启动时会自动生成证书并创建 `MutatingWebhookConfiguration`（含 caBundle），无需再手动 apply `mutating-webhook-config.yaml`。

### 4. 等待 Pod 就绪

```bash
kubectl -n mutating-webhook-demo get pods -w
# 等 STATUS 为 Running、READY 为 1/1
```

确认配置已创建：

```bash
kubectl get mutatingwebhookconfiguration mutate-pod-demo -o yaml
# 应能看到 webhooks[0].clientConfig.service.namespace=mutating-webhook-demo 且 clientConfig.caBundle 非空
```

### 5. 验证 Mutating Webhook

在**任意 namespace** 创建 Pod（例如 default），检查是否被注入 label：

```bash
kubectl run nginx --image=nginx
kubectl get pod nginx -o jsonpath='{.metadata.labels.mutated-by}'
# 应输出: my-webhook
```

再确认 Pod 的 label 列表：

```bash
kubectl get pod nginx -o yaml | grep -A5 labels
```

### 6. 清理（可选）

```bash
kubectl delete -f deploy/
kubectl delete mutatingwebhookconfiguration mutate-pod-demo
k3d cluster delete mycluster
```

### 7. 故障排查：`make verify` 与 Webhook TLS 错误

**`make verify` 在做什么？**

1. 删除可能存在的旧测试 Pod `verify-nginx`。
2. 执行 `kubectl run verify-nginx --image=nginx`，在 default namespace 创建一个 Pod。
3. API Server 收到创建 Pod 的请求后，根据 `MutatingWebhookConfiguration` 向 Webhook 发起 HTTPS 调用：  
   `https://mutating-webhook-demo.mutating-webhook-demo.svc:9443/mutate-v1-pod`
4. API Server 作为 TLS **客户端**，用配置里的 **caBundle** 校验 Webhook 的服务器证书；同时会校验证书的 **SAN（Subject Alternative Name）** 是否包含当前访问的域名（即上述 `mutating-webhook-demo.mutating-webhook-demo.svc`）。Go 1.15+ 的 TLS 已不再用 CN，只看 SAN。
5. 若证书里没有这个 DNS 名 → 报错：`x509: certificate is not valid for any names, but wanted to match mutating-webhook-demo.mutating-webhook-demo.svc`。
6. 若 TLS 通过，Webhook 返回修改后的 Pod（带 `mutated-by=my-webhook`），API Server 再完成创建；`make verify` 检查 label 并删除测试 Pod。

**为何会出现 “calling webhook” 的 TLS 报错？**

- 我们的 Webhook 在启动时用 **certs.go** 生成自签证书并写入 `/etc/webhook-certs`。
- 证书的 **DNSNames** 必须包含 API Server 实际访问的域名：  
  `mutating-webhook-demo.mutating-webhook-demo.svc` 和 `mutating-webhook-demo.mutating-webhook-demo.svc.cluster.local`。
- 若证书里没有这些名字（例如用的是旧镜像、旧代码里没有写 DNSNames），TLS 校验会失败，就会出现你看到的错误。

**处理方式：**

- 确保当前 **certs.go** 里 `certTemplate` 的 **DNSNames** 包含上述 `svcShort` 和 `svcFQDN`（当前代码已包含）。
- **重新构建镜像并让新 Pod 用新证书**：  
  `make dev` 或 `make image-import && make restart`。  
  新 Pod 启动时会重新生成带正确 DNSNames 的证书，并更新 `MutatingWebhookConfiguration` 的 caBundle，之后 `make verify` 即可通过。

---

## 本地编译与运行（不部署到集群）

适用于本地调试或配合 envtest 测试：

```bash
cd mutating-webhook-demo
go build -o mutating-webhook-demo .
./mutating-webhook-demo
```

进程会：

- 在 **9443** 端口提供 Webhook **HTTPS** 服务（证书来自 `WEBHOOK_CERT_DIR` 或默认 `/etc/webhook-certs`，缺失则自动生成）；
- 在 **8081** 提供健康检查 `:8081/healthz`。

若希望该进程同时向**当前 kubeconfig 指向的集群**注册 MutatingWebhookConfiguration（并写入 caBundle），需集群可写且具备 RBAC，然后：

```bash
export NAMESPACE=mutating-webhook-demo   # 与后续部署的 Service 所在 namespace 一致
export INSTALL_WEBHOOK_CONFIG=1
./mutating-webhook-demo
```

此时需保证集群内已有同名 Service（例如通过 `kubectl apply -f deploy/` 只部署 Service），否则 API Server 无法解析 webhook 地址。

---

## 手动 apply MutatingWebhookConfiguration

若**不**使用程序安装（即未设置 `INSTALL_WEBHOOK_CONFIG=1`），可手动 apply 配置，但必须自行填写 **caBundle**（Webhook 的 CA 证书 PEM 的 base64），否则 API Server 不信任 TLS：

```bash
# 1. 从已运行 Webhook 的 Pod 中取出 CA（示例）
kubectl -n mutating-webhook-demo exec deploy/mutating-webhook-demo -- cat /etc/webhook-certs/tls.crt | base64 -w0
# 2. 将输出填入 mutating-webhook-config.yaml 的 webhooks[0].clientConfig.caBundle，再 apply
kubectl apply -f mutating-webhook-config.yaml
```

`mutating-webhook-config.yaml` 中 `clientConfig.service.namespace` 已为 `mutating-webhook-demo`，与 `deploy/` 一致。

---

## 使用 envtest 本地测试

使用 controller-runtime 的 envtest 可在本地起一个真实 API Server，并注入 Webhook 配置与 CA，无需部署到远程集群。可参考 [controller-runtime envtest 文档](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest) 写一个 `*_test.go`，在测试中启动 envtest、将 `mutatingConfig()` 的 `ClientConfig` 指向 envtest 提供的 Webhook 安装 URL、启动本示例的 Manager/WebhookServer，然后创建 Pod 并断言 `metadata.labels["mutated-by"] == "my-webhook"`。

---

## 与 persephone 的对应关系

- **GetMutatingWebhookConfiguration()** → 本示例的 **mutatingConfig()**
- **getClientConfig(WebhookPath)** → 本示例在 **mutatingConfig()** 内直接写 `ClientConfig`（Service + 可选 caBundle）
- **AddToManager 中 Register(WebhookPath, webhook)** → **addPodMutator(mgr)** 中的 **Register(mutatePodPath, wh)**
- **admission.WithCustomDefaulter(..., Shoot, h)** → **admission.WithCustomDefaulter(..., &corev1.Pod{}, &podMutator{})**
- **Handler.Default(ctx, obj)** 修改 Shoot → **podMutator.Default(ctx, obj)** 给 Pod 添加 label
- **证书管理**：persephone 使用 Gardener extensions 的证书 controller；本示例在 **certs.go** 中自签并写入 caBundle


## Controller-runtime 的方式

```
  func addPodMutator(mgr manager.Manager) error {
      // 一步到位！所有的脏活累活都帮你做了
      wh := admission.WithCustomDefaulter(scheme, &corev1.Pod{}, &podMutator{})
      mgr.GetWebhookServer().Register(mutatePodPath, wh)
      return nil
  }
```

## 它帮你做了什么？

 传统方式                                                  │ Controller-runtime
───────────────────────────────────────────────────────────┼──────────────────────────────────────────────────────────
 手动创建 HTTP 服务器                                      │ ✅ 自动管理
 解析 JSON 请求                                            │ ✅ 自动解析
 提取 Pod 对象                                             │ ✅ 自动提取
 构造 AdmissionReview 响应                                 │ ✅ 自动构造
 JSON 序列化                                               │ ✅ 自动序列化
 处理 panic                                                │ ✅  WithRecoverPanic()
 TLS 证书管理                                              │ ✅ 自动管理
 Kubernetes API 类型转换                                   │ ✅ 使用  scheme
# mutating-webhook-demo
# mutating-webhook-demo
