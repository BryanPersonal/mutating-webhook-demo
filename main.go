// 最小可运行的 MutatingWebhook 示例：对 Pod 的 Create/Update 自动添加一个 label。
// 对应 persephone 中 cmd/persephone-webhook 的启动与注册流程。
// 仓库结构与核心设计说明见 README.md、docs/DESIGN.md。
package main

import (
	"context"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	certDir := os.Getenv("WEBHOOK_CERT_DIR")
	if certDir == "" {
		certDir = defaultCertDir
	}
	caPEM, err := ensureCertDir(certDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ensure cert dir: %v\n", err)
		os.Exit(1)
	}
	// ctrl.GetConfigorDie() 返回一个 *rest.Config，用于与 Kubernetes API 服务器通信。
	// equals to rest.InClusterConfig()
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    9443,
			CertDir: certDir,
		}),
		HealthProbeBindAddress: ":8081",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "new manager: %v\n", err)
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		fmt.Fprintf(os.Stderr, "add healthz: %v\n", err)
		os.Exit(1)
	}

	// 对应 persephone 的 AddToManager：把 Mutating Handler 挂到 WebhookServer 的路径上
	if err := addPodMutator(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "add pod mutator: %v\n", err)
		os.Exit(1)
	}

	// 将 MutatingWebhookConfiguration 的安装放到 Manager 启动后的 Runnable 中执行，
	// 否则 GetClient().Get() 会报 "the cache is not started, can not read objects"（cache 在 mgr.Start() 后才启动）
	if os.Getenv("INSTALL_WEBHOOK_CONFIG") == "1" {
		if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
			return installMutatingConfig(ctx, mgr, caPEM)
		})); err != nil {
			fmt.Fprintf(os.Stderr, "add install runnable: %v\n", err)
			os.Exit(1)
		}
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "manager start: %v\n", err)
		os.Exit(1)
	}
}
