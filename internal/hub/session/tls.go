package session

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// 自签证书文件名（相对 data_dir，SPEC 二：TLS 证书 hub 首次启动自签生成存 data/）。
const (
	certFileName = "hub_cert.pem"
	keyFileName  = "hub_key.pem"
)

// certValidity 自签证书有效期。
const certValidity = 10 * 365 * 24 * time.Hour

// EnsureCert 取得 hub TLS 证书：data_dir 下已有证书则加载复用（重启不换证书，
// node 侧指纹才稳定），否则生成自签证书（ECDSA P-256，crypto/x509）并以
// 临时文件+rename 原子落盘（CLAUDE.md：禁直接覆盖写）。返回证书与私钥文件路径。
func EnsureCert(dataDir string) (certPath, keyPath string, err error) {
	certPath = filepath.Join(dataDir, certFileName)
	keyPath = filepath.Join(dataDir, keyFileName)
	if _, statErr := os.Stat(certPath); statErr == nil {
		if _, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
			return certPath, keyPath, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("session: 生成 ECDSA P-256 私钥失败: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("session: 生成序列号失败: %w", err)
	}
	now := time.Now().UTC()
	tpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "swarmmind-hub", Organization: []string{"swarmmind"}},
		NotBefore:             now.Add(-time.Hour), // 容时钟偏差
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("session: 自签证书失败: %w", err)
	}
	if err := atomicWriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", fmt.Errorf("session: 写证书失败: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("session: 编码私钥失败: %w", err)
	}
	if err := atomicWriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return "", "", fmt.Errorf("session: 写私钥失败: %w", err)
	}
	return certPath, keyPath, nil
}

// atomicWriteFile 临时文件+rename 原子写（同目录，保证替换原子）。
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ValidateLoopback 校验监听地址 host 为回环（127.0.0.1/::1）。insecure_loopback=true
// 时强制（代码强制，非文档约束，SPEC 二）：明文 ws 只允许跑在回环上，监听空 host
// （全接口）一律拒绝启动。
func ValidateLoopback(listenAddr string) error {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("session: 监听地址非法 %q: %w", listenAddr, err)
	}
	if host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("session: insecure_loopback 模式监听地址必须为回环（127.0.0.1/::1），got %q", listenAddr)
}

// ValidateAPIListen 校验管理面（api_listen）监听地址只能绑定 127.0.0.1（C4/SPEC 二）：
// 无条件、与 TLS 模式无关——管理面无鉴权，禁止 0.0.0.0/公网地址/::1/空 host（全接口）。
func ValidateAPIListen(listenAddr string) error {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("session: api_listen 地址非法 %q: %w", listenAddr, err)
	}
	if host == "127.0.0.1" {
		return nil
	}
	return fmt.Errorf("session: 管理面 api_listen 必须绑定 127.0.0.1（无鉴权入口禁止对外），got %q", listenAddr)
}

// LoadCertificate 加载（或首次生成）证书为 tls.Certificate，供 TLS 监听使用。
func LoadCertificate(dataDir string) (tls.Certificate, error) {
	certPath, keyPath, err := EnsureCert(dataDir)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("session: 加载证书失败: %w", err)
	}
	return cert, nil
}

// errMissingDataDir data_dir 为空时的启动错误。
var errMissingDataDir = errors.New("session: data_dir 未配置")
