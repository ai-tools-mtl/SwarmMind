package conn

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
)

// TLS 指纹固定与 TOFU（SPEC 二）：node 侧 hub 身份校验。
//   - hub_pubkey_fp 非空：强校验模式——TLS 握手时比对对端证书 SPKI 的 SHA-256
//     （base64 标准编码），不匹配即握手失败，register_token 永不出本机；
//   - 为空：TOFU 模式——首连成功把 host:port→fp 记入 data_dir/trusted_hubs.json，
//     此后不匹配即断开告警；记录写失败仅告警不阻断（SPEC 二）。
//   - 证书链校验被指纹校验替代（自签场景：InsecureSkipVerify + 自定义指纹校验，
//     即固定 pin 模式），带外分发指纹是信任根。

// trustFileName TOFU 记录文件名（相对 node data_dir，SPEC 二）。
const trustFileName = "trusted_hubs.json"

// FingerprintSPKI 证书 SPKI 的 SHA-256 指纹：base64 标准编码（与
// `openssl x509 -pubkey | openssl pkey -pubin -outform der | openssl dgst -sha256
// -binary | base64` 同口径）。
func FingerprintSPKI(rawSubjectPublicKeyInfo []byte) string {
	sum := sha256.Sum256(rawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// VerifyFingerprint 强校验模式：fp 与期望值恒时比对；不一致即拒绝（返回错误，
// TLS 握手失败，连接建立前 register_token 不出本机）。
func VerifyFingerprint(fp, expect string) error {
	if subtle.ConstantTimeCompare([]byte(fp), []byte(expect)) != 1 {
		return fmt.Errorf("hub 证书指纹不匹配：期望 %s 实得 %s（拒绝连接，请核对 hub_pubkey_fp 或警惕中间人）", expect, fp)
	}
	return nil
}

// TrustStore TOFU 信任记录：data_dir/trusted_hubs.json，host:port → 指纹。
// 惰性加载 + 互斥；写入为临时文件+rename 原子替换（CLAUDE.md：禁直接覆盖写）。
// 写失败由调用方仅告警（SPEC 二：TOFU 记录写失败仅告警不阻断）。
type TrustStore struct {
	path string

	mu      sync.Mutex
	loaded  bool
	entries map[string]string
}

// OpenTrustStore 打开（不立即读盘）TOFU 记录，存于 dataDir/trusted_hubs.json。
func OpenTrustStore(dataDir string) *TrustStore {
	return &TrustStore{path: filepath.Join(dataDir, trustFileName)}
}

// Get 查询 host:port 的已记录指纹。
func (t *TrustStore) Get(hostport string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.loadLocked(); err != nil {
		return "", false // 记录损坏/不可读：按无记录处理（首连语义），Put 时覆盖
	}
	fp, ok := t.entries[hostport]
	return fp, ok
}

// Put 记录（或更新）host:port → 指纹并原子落盘。
func (t *TrustStore) Put(hostport, fp string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.loadLocked(); err != nil {
		return err
	}
	t.entries[hostport] = fp
	b, err := json.MarshalIndent(t.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

// loadLocked 读盘（惰性一次）；文件不存在视为空记录。
func (t *TrustStore) loadLocked() error {
	if t.loaded {
		return nil
	}
	b, err := os.ReadFile(t.path)
	if os.IsNotExist(err) {
		t.entries = map[string]string{}
		t.loaded = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("conn: 读取 TOFU 记录失败: %w", err)
	}
	entries := map[string]string{}
	if err := json.Unmarshal(b, &entries); err != nil {
		return fmt.Errorf("conn: TOFU 记录损坏 %s: %w", t.path, err)
	}
	t.entries = entries
	t.loaded = true
	return nil
}

// trustHostPort 从已规范化的 hub URL 提取 host:port（TOFU 记录键；无显式端口按
// scheme 默认：wss→443）。
func trustHostPort(normalizedURL string) (string, error) {
	p, err := url.Parse(normalizedURL)
	if err != nil {
		return "", fmt.Errorf("conn: hub_url 非法 %q: %w", normalizedURL, err)
	}
	host := p.Hostname()
	if host == "" {
		return "", fmt.Errorf("conn: hub_url 缺 host %q", normalizedURL)
	}
	if port := p.Port(); port != "" {
		return host + ":" + port, nil
	}
	return host + ":443", nil
}
