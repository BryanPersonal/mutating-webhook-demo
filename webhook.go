// webhook.go 对应 persephone 的 internal/webhook/add.go + shootadmission/add.go：
// - addPodMutator: 在 WebhookServer 上注册路径与 CustomDefaulter
// - mutatingConfig: 返回要写入集群的 MutatingWebhookConfiguration 模板
// - installMutatingConfig: 可选，将配置安装到当前集群
package main

import (
	"context"
	"os"

	corev1 "k8s.io/api/core/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	// mutatePodPath 对应 persephone 的 shootadmission.WebhookPath，必须与 MutatingWebhookConfiguration 的 ClientConfig 一致
	mutatePodPath = "/mutate-v1-pod"
	labelKey      = "mutated-by"
	labelValue    = "my-webhook"
)

// addPodMutator 对应 persephone 的 AddToManager 里注册 shootadmission 的那段：
// 使用 WithCustomDefaulter 得到 admission Webhook，并注册到 WebhookServer 的 mutatePodPath
func addPodMutator(mgr manager.Manager) error {
	wh := admission.WithCustomDefaulter(scheme, &corev1.Pod{}, &podMutator{}).WithRecoverPanic(true)
	mgr.GetWebhookServer().Register(mutatePodPath, wh)
	return nil
}

// podMutator 实现 admission.CustomDefaulter：在 Create/Update 时修改 Pod（此处仅添加一个 label）
type podMutator struct{}

func (p *podMutator) Default(ctx context.Context, obj runtime.Object) error {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[labelKey] = labelValue
	return nil
}

// namespaceExcludeLabel 用于排除 Webhook 自身所在 namespace，避免创建/重启 Webhook Pod 时也调用 Webhook（导致 TLS 失败）。
const namespaceExcludeLabel = "mutating-webhook-demo.io/exclude"

// mutatingConfig 对应 persephone 的 GetMutatingWebhookConfiguration()：
// 返回要写入集群的 MutatingWebhookConfiguration，告诉 API Server
// 对 core/v1 Pod 的 Create/Update 请求发到 mutatePodPath。
// NamespaceSelector 排除带 namespaceExcludeLabel 的 namespace，避免 Webhook 自己的 Pod 创建时也走 Webhook。
func mutatingConfig() *admissionregistrationv1.MutatingWebhookConfiguration {
	ns := os.Getenv("NAMESPACE")
	if ns == "" {
		ns = "default"
	}
	path := mutatePodPath
	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "mutate-pod-demo"},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name:                    "mutate-pod.webhook.demo",
			AdmissionReviewVersions: []string{"v1"},
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{
					Namespace: ns,
					Name:      "mutating-webhook-demo",
					Path:      &path,
					Port:      ptr.To(int32(9443)),
				},
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{corev1.SchemeGroupVersion.Group},
					APIVersions: []string{corev1.SchemeGroupVersion.Version},
					Resources:   []string{"pods"},
				},
				Operations: []admissionregistrationv1.OperationType{
					admissionregistrationv1.Create,
					admissionregistrationv1.Update,
				},
			}},
			// 排除带 exclude label 的 namespace，避免本 namespace 内 Pod（含 Webhook 自己）创建时也调 Webhook，导致重启时 TLS 失败
			NamespaceSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      namespaceExcludeLabel,
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{"true"},
				}},
			},
			SideEffects:    ptr.To(admissionregistrationv1.SideEffectClassNoneOnDryRun),
			FailurePolicy:  ptr.To(admissionregistrationv1.Fail),
			MatchPolicy:    ptr.To(admissionregistrationv1.Exact),
			TimeoutSeconds: ptr.To(int32(10)),
		}},
	}
}

// installMutatingConfig 将 MutatingWebhookConfiguration 创建或更新到当前集群，并写入 caBundle。
// 若配置已存在（例如重启后），则 Patch 更新 caBundle，使新 Pod 的证书被 API Server 信任。
func installMutatingConfig(ctx context.Context, mgr manager.Manager, caPEM []byte) error {
	cfg := mutatingConfig()
	if len(caPEM) > 0 {
		cfg.Webhooks[0].ClientConfig.CABundle = caPEM
	}
	cli := mgr.GetClient()
	existing := &admissionregistrationv1.MutatingWebhookConfiguration{}
	err := cli.Get(ctx, client.ObjectKey{Name: cfg.Name}, existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return cli.Create(ctx, cfg)
		}
		return err
	}
	existing.Webhooks = cfg.Webhooks
	return cli.Update(ctx, existing)
}
