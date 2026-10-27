package api

// 管理员账户（2026-10-21 验收反馈：令牌不应承担登录职责）。
//
// 模型（对齐 GitHub 心智）：
//   - 出厂默认：无账户，仅有 admin_token（root 凭据，印在 hub.yaml）；
//   - 初始化：admin_token 登录后引导「设置用户名与密码」→ 生成账户；
//   - 日常登录：用户名 + 密码 → 会话 cookie（与令牌登录同会话体系）；
//   - 账户设置：改用户名、改密码（需旧密码验证）；
//   - 恢复通道：忘记密码 → 用 admin_token 登录 → 重置密码（root 永不失联）；
//   - 令牌（PAT）回归纯 API 凭证，与登录解耦。
//
// 密码哈希：PBKDF2-HMAC-SHA256（RFC 8018，标准库手写，无新依赖），
// 210_000 轮（OWASP 2023 建议），16 字节随机盐。存储 data_dir/account.json（0600 原子写）。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	pbkdf2Rounds = 210_000
	saltLen      = 16
	hashLen      = 32
	minPassword  = 8
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)

var (
	// ErrNoAccount 账户尚未初始化。
	ErrNoAccount = errors.New("账户尚未初始化")
	// ErrBadCredentials 用户名或密码错误。
	ErrBadCredentials = errors.New("用户名或密码错误")
	// ErrAlreadyInitialized 账户已存在（不可重复 setup）。
	ErrAlreadyInitialized = errors.New("账户已初始化，如需重置请先登录或使用恢复凭据")
)

// account 管理员账户持久化形态。
type account struct {
	Username  string    `json:"username"`
	Salt      string    `json:"salt"` // hex
	Hash      string    `json:"hash"` // hex（PBKDF2 输出）
	Rounds    int       `json:"rounds"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// accountStore 单管理员账户（V0 单账户；多账户属 V2）。
type accountStore struct {
	mu   sync.Mutex
	path string
	acct *account // nil = 未初始化
}

func newAccountStore(dataDir string) *accountStore {
	as := &accountStore{path: filepath_join(dataDir, "account.json")}
	if b, err := os.ReadFile(as.path); err == nil {
		var a account
		if json.Unmarshal(b, &a) == nil && a.Username != "" {
			as.acct = &a
		}
	}
	return as
}

func filepath_join(elem ...string) string { return strings.Join(elem, string(os.PathSeparator)) }

func (as *accountStore) persist() error {
	b, err := json.MarshalIndent(as.acct, "", "  ")
	if err != nil {
		return err
	}
	tmp := as.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, as.path)
}

// initialized 是否已设置账户。
func (as *accountStore) initialized() bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.acct != nil
}

// setup 初始化账户（仅首次；已存在则报错）。
func (as *accountStore) setup(username, password string, now time.Time) error {
	if !usernameRe.MatchString(username) {
		return errors.New("用户名须为 3-32 位字母/数字/下划线")
	}
	if len(password) < minPassword {
		return errors.New("密码至少 8 位")
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.acct != nil {
		return ErrAlreadyInitialized
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	as.acct = &account{
		Username:  username,
		Salt:      hexEnc(salt),
		Hash:      hexEnc(pbkdf2([]byte(password), salt, pbkdf2Rounds, hashLen)),
		Rounds:    pbkdf2Rounds,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
	}
	return as.persist()
}

// loginPassword 用户名+密码登录校验。
func (as *accountStore) loginPassword(username, password string) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.acct == nil {
		return false
	}
	a := as.acct
	// 用户名常数时间比较 + 哈希常数时间比较（防时序侧信道；用户名非机密，规范化防御即可）
	if subtle.ConstantTimeCompare([]byte(a.Username), []byte(username)) != 1 {
		return false
	}
	salt, err1 := hexDec(a.Salt)
	want, err2 := hexDec(a.Hash)
	if err1 != nil || err2 != nil {
		return false
	}
	got := pbkdf2([]byte(password), salt, a.Rounds, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// updateCredentials 改用户名（需密码验证）。
func (as *accountStore) updateUsername(currentPassword, newUsername string, now time.Time) error {
	if !usernameRe.MatchString(newUsername) {
		return errors.New("用户名须为 3-32 位字母/数字/下划线")
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.acct == nil {
		return ErrNoAccount
	}
	if !as.loginPasswordLocked(currentPassword) {
		return ErrBadCredentials
	}
	as.acct.Username = newUsername
	as.acct.UpdatedAt = now.UTC()
	return as.persist()
}

// changePassword 改密码（需旧密码；新密码不能与旧相同）。
func (as *accountStore) changePassword(currentPassword, newPassword string, now time.Time) error {
	if len(newPassword) < minPassword {
		return errors.New("密码至少 8 位")
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.acct == nil {
		return ErrNoAccount
	}
	if !as.loginPasswordLocked(currentPassword) {
		return ErrBadCredentials
	}
	if subtle.ConstantTimeCompare([]byte(currentPassword), []byte(newPassword)) == 1 {
		return errors.New("新密码不能与当前密码相同")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	as.acct.Salt = hexEnc(salt)
	as.acct.Hash = hexEnc(pbkdf2([]byte(newPassword), salt, pbkdf2Rounds, hashLen))
	as.acct.Rounds = pbkdf2Rounds
	as.acct.UpdatedAt = now.UTC()
	return as.persist()
}

// resetPasswordRoot admin_token（root）重置密码/用户名——恢复通道。
func (as *accountStore) resetPasswordRoot(newUsername, newPassword string, now time.Time) error {
	if !usernameRe.MatchString(newUsername) {
		return errors.New("用户名须为 3-32 位字母/数字/下划线")
	}
	if len(newPassword) < minPassword {
		return errors.New("密码至少 8 位")
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.acct == nil {
		return ErrNoAccount
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	as.acct.Username = newUsername
	as.acct.Salt = hexEnc(salt)
	as.acct.Hash = hexEnc(pbkdf2([]byte(newPassword), salt, pbkdf2Rounds, hashLen))
	as.acct.Rounds = pbkdf2Rounds
	as.acct.UpdatedAt = now.UTC()
	return as.persist()
}

// loginPasswordLocked 已持锁的密码校验。
func (as *accountStore) loginPasswordLocked(password string) bool {
	a := as.acct
	salt, err1 := hexDec(a.Salt)
	want, err2 := hexDec(a.Hash)
	if err1 != nil || err2 != nil {
		return false
	}
	got := pbkdf2([]byte(password), salt, a.Rounds, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// view 脱敏信息（无哈希/盐）。
func (as *accountStore) view() map[string]any {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.acct == nil {
		return map[string]any{"initialized": false}
	}
	return map[string]any{
		"initialized": true,
		"username":    as.acct.Username,
		"created_at":  as.acct.CreatedAt,
		"updated_at":  as.acct.UpdatedAt,
	}
}

/* ---------- PBKDF2-HMAC-SHA256（RFC 8018，标准库实现，避免新增 x/crypto 依赖） ---------- */

func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	U := make([]byte, hashLen)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		prf.Write(buf[:])
		dk = prf.Sum(dk)
		T := dk[len(dk)-hashLen:]
		copy(U, T)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(U)
			U = U[:0]
			U = prf.Sum(U)
			for x := range U {
				T[x] ^= U[x]
			}
		}
	}
	return dk[:keyLen]
}

func hexEnc(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0x0f]
	}
	return string(out)
}

func hexDec(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errors.New("bad hex")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, ok1 := hexVal(s[i*2])
		lo, ok2 := hexVal(s[i*2+1])
		if !ok1 || !ok2 {
			return nil, errors.New("bad hex")
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

/* ---------- HTTP 端点 ---------- */

// handleAccountStatus GET /api/v1/auth/account：账户状态。
// 未登录（无有效会话）：仅返回 {initialized}（登录页判断用，不泄漏用户名，
// 也不返回 server_version / terminal_retention_sec——版本号与保留期不对未认证方披露）；
// 已登录：返回完整账户信息 + server_version + terminal_retention_sec（控制台
// 页脚与保留期文案数据源，会话恢复时前端经此端点取值）。
func (s *Server) handleAccountStatus(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		s.mu.Lock()
		exp, ok := s.sessions[c.Value]
		s.mu.Unlock()
		if ok && time.Now().UTC().Before(exp) {
			v := s.accounts.view()
			v["server_version"] = s.cfg.Version
			v["terminal_retention_sec"] = s.terminalRetentionSec() // 登录态账户视图同面下发（会话恢复时前端经此取保留期，maturity-retention-config）
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"initialized": s.accounts.initialized()})
}

// handleAccountSetup POST /api/v1/auth/setup：首次初始化。
// 放行条件：账户未初始化（任何人可抢注首账户——中心端回环/私有网部署语义），
// 或携带 admin_token（X-Admin-Token 头）。
func (s *Server) handleAccountSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON")
		return
	}
	if s.accounts.initialized() {
		// 已初始化：仅 admin_token（root）可重置
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(s.cfg.AdminToken)) != 1 || s.cfg.AdminToken == "" {
			writeErr(w, http.StatusConflict, "already_initialized", "账户已存在；如需重置请用管理令牌（X-Admin-Token）")
			return
		}
		if err := s.accounts.resetPasswordRoot(req.Username, req.Password, time.Now()); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reset_by": "admin_token"})
		return
	}
	if err := s.accounts.setup(req.Username, req.Password, time.Now()); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "username": req.Username})
}

// handleLoginPassword POST /api/v1/auth/login-password：用户名+密码 → 会话 cookie。
func (s *Server) handleLoginPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON")
		return
	}
	if !s.accounts.loginPassword(req.Username, req.Password) {
		time.Sleep(300 * time.Millisecond) // 暴力破解减速
		writeErr(w, http.StatusUnauthorized, "auth_failed", "用户名或密码错误")
		return
	}
	s.mu.Lock()
	sid, err := newSessionID()
	if err == nil {
		s.sessions[sid] = time.Now().UTC().Add(sessionTTL)
	}
	s.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "会话创建失败")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL / time.Second),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_in": int(sessionTTL / time.Second), "server_version": s.cfg.Version, "terminal_retention_sec": s.terminalRetentionSec()})
}

// handleAccountUpdate POST /api/v1/auth/account/update：改用户名/改密码（密码验证）。
func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewUsername     string `json:"new_username,omitempty"`
		NewPassword     string `json:"new_password,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON")
		return
	}
	now := time.Now()
	if req.NewUsername != "" {
		if err := s.accounts.updateUsername(req.CurrentPassword, req.NewUsername, now); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	if req.NewPassword != "" {
		if err := s.accounts.changePassword(req.CurrentPassword, req.NewPassword, now); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	if req.NewUsername == "" && req.NewPassword == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "new_username 与 new_password 至少填一项")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": s.accounts.view()})
}
