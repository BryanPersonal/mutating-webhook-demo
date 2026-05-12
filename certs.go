// certs.go 在 CertDir 中生成自签证书（tls.crt / tls.key），供 WebhookServer 使用；
// API Server 需通过 MutatingWebhookConfiguration 的 caBundle 信任该证书（本示例在安装配置时写入 caBundle）。
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultCertDir = "/etc/webhook-certs"
	certFileName   = "tls.crt"
	keyFileName    = "tls.key"
)

const (
	serviceName = "mutating-webhook-demo"
)

// ensureCertDir 在 certDir 中生成自签证书（若不存在），并返回 CA 证书的 PEM，用于 caBundle。
// 证书的 DNSNames 包含 Kubernetes Service 的 FQDN，以便 API Server 调用时 TLS 校验通过。
func ensureCertDir(certDir string) (caPEM []byte, err error) {
	if err := os.MkdirAll(certDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir cert dir: %w", err)
	}
	certPath := filepath.Join(certDir, certFileName)
	keyPath := filepath.Join(certDir, keyFileName)
	if _, err := os.Stat(certPath); err == nil {
		// 已存在，只读 CA 用于 caBundle（自签时证书即 CA）
		return os.ReadFile(certPath)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "mutating-webhook-demo"
	}
	template, err := certTemplate(namespace)
	if err != nil {
		return nil, err
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return nil, fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	return certPEM, nil
}

func certTemplate(namespace string) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	// API Server 调用 Webhook 时使用 Service DNS：
	//   <service>.<namespace>.svc 或 <service>.<namespace>.svc.cluster.local
	// TLS 校验证书时只认 SAN (DNSNames)，不认 CN，因此必须把上述名字写进 DNSNames。
	svcShort := serviceName + "." + namespace + ".svc"
	svcFQDN := serviceName + "." + namespace + ".svc.cluster.local"
	return &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: serviceName},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{serviceName, svcShort, svcFQDN, "localhost"},
	}, nil
}
