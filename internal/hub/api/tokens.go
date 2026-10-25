package api

// 访问令牌管理（2026-10-21 验收反馈：仿 GitHub PAT 机制）。
//
// 模型：管理员用 admin_token 登录后，可创建/列出/撤销子令牌（PAT）。
//   - 明文格式：smsw_<43 字符 base64url>，仅在创建响应中出现一次；
//   - 存储只存 SHA-256 哈希与前缀（泄漏数据库不可还原明文）；
//   - 鉴权：Authorization: Bearer <明文> 与会话 cookie 二选一；
//   - 撤销为软删（revoked=true），保留审计痕迹；重启不丢失（data_dir/api_tokens.json）。
//
// 原子写对齐项目规范（temp + rename）；文件 0600。

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// APIToken 一条子令牌记录（持久化形态）。
type APIToken struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Prefix     string    `json:"prefix"` // 明文前 13 字符（smsw_xxxxxxx），展示用
	Hash       string    `json:"hash"`   // sha256 hex（明文）
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
	Revoked    bool      `json:"revoked"`
}

// tokenStore 内存态 + 磁盘持久化（挂到 Server 生命周期）。
type tokenStore struct {
	mu   sync.Mutex
	path string
	list []APIToken
}

const tokenPrefixPlain = "smsw_"

func newTokenStore(dataDir string) *tokenStore {
	ts := &tokenStore{path: filepath.Join(dataDir, "api_tokens.json")}
	if b, err := os.ReadFile(ts.path); err == nil {
		_ = json.Unmarshal(b, &ts.list) // 损坏文件视为空表（不阻断启动）
	}
	return ts
}

// persist 原子写（temp + rename，0600）。
func (ts *tokenStore) persist() error {
	b, err := json.MarshalIndent(ts.list, "", "  ")
	if err != nil {
		return err
	}
	tmp := ts.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ts.path)
}

// create 生成新令牌，返回 (记录, 明文)。明文仅此处返回一次。
func (ts *tokenStore) create(name string, now time.Time) (APIToken, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return APIToken{}, "", err
	}
	plain := tokenPrefixPlain + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(plain))
	id := "tk-" + hex.EncodeToString(sum[:8])
	t := APIToken{
		ID:        id,
		Name:      name,
		Prefix:    plain[:13],
		Hash:      hex.EncodeToString(sum[:]),
		CreatedAt: now.UTC(),
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.list = append(ts.list, t)
	sort.Slice(ts.list, func(i, j int) bool { // 新的在前
		return ts.list[i].CreatedAt.After(ts.list[j].CreatedAt)
	})
	return t, plain, ts.persist()
}

// verify 校验明文令牌；命中且未撤销 → 更新 last_used，返回 (令牌名, true)。
// 令牌名即服务端验证的凭据主体（事件账本 principal 用，RFC 六.2：与请求自报
// 标签分开存储；重名以令牌列表前缀人工区分，V0 简化）。
func (ts *tokenStore) verify(plain string) (string, bool) {
	if !strings.HasPrefix(plain, tokenPrefixPlain) {
		return "", false
	}
	sum := sha256.Sum256([]byte(plain))
	want := hex.EncodeToString(sum[:])
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := time.Now().UTC()
	for i := range ts.list {
		t := &ts.list[i]
		if t.Revoked || subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want)) != 1 {
			continue
		}
		t.LastUsedAt = now
		_ = ts.persist() // 尽力而为；失败不影响本次鉴权
		return t.Name, true
	}
	return "", false
}

// list 返回脱敏副本（不含哈希）。
func (ts *tokenStore) listSafe() []APIToken {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]APIToken, len(ts.list))
	copy(out, ts.list)
	for i := range out {
		out[i].Hash = ""
	}
	return out
}

// revoke 软撤销。
func (ts *tokenStore) revoke(id string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i := range ts.list {
		if ts.list[i].ID == id && !ts.list[i].Revoked {
			ts.list[i].Revoked = true
			return ts.persist() == nil
		}
	}
	return false
}

// handleTokenCreate POST /api/v1/auth/tokens：{name} → 201 {id,name,token,...}。
func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "name 必填（令牌用途名称）")
		return
	}
	t, plain, err := s.tokens.create(strings.TrimSpace(req.Name), time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "令牌创建失败")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": t.ID, "name": t.Name, "prefix": t.Prefix,
		"token": plain, "created_at": t.CreatedAt,
		"notice": "明文仅此一次展示，请立即保存；之后只能看到前缀。",
	})
}

// handleTokenList GET /api/v1/auth/tokens：脱敏列表。
func (s *Server) handleTokenList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tokens": s.tokens.listSafe()})
}

// handleTokenRevoke DELETE /api/v1/auth/tokens/{id}：软撤销。
func (s *Server) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.tokens.revoke(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "token_not_found", "令牌不存在或已撤销")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
