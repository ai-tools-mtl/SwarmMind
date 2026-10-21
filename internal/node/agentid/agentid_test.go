package agentid

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWorkspaceIDShapeAndStable 键形态：16 字符小写 hex，且对同一路径确定性可复现。
func TestWorkspaceIDShapeAndStable(t *testing.T) {
	dir := t.TempDir()
	id1 := WorkspaceID(dir)
	id2 := WorkspaceID(dir)
	if len(id1) != workspaceIDLen {
		t.Fatalf("键长应 %d 字符, got %d (%q)", workspaceIDLen, len(id1), id1)
	}
	if id1 != id2 {
		t.Fatalf("同路径两次计算应一致: %q vs %q", id1, id2)
	}
	for _, r := range id1 {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			t.Fatalf("应为小写 hex, got %q", id1)
		}
	}
	// 派生口径：sha256(规范化路径) 前 16 字符（锁定公式，防实现漂移）
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(canonical))
	if want := hex.EncodeToString(sum[:])[:workspaceIDLen]; id1 != want {
		t.Fatalf("派生口径不符: got %q want %q", id1, want)
	}
}

// TestWorkspaceIDSymlinkAlias symlink 别名同键（P1.5 核心：消解别名防同目录双键）；
// 不同目录不同键。
func TestWorkspaceIDSymlinkAlias(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "workspace")
	if err := mkdir(real); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "workspace-link")
	if err := symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got, want := WorkspaceID(link), WorkspaceID(real); got != want {
		t.Fatalf("symlink 别名应同键: link=%q real=%q", got, want)
	}
	other := filepath.Join(base, "other")
	if err := mkdir(other); err != nil {
		t.Fatal(err)
	}
	if got := WorkspaceID(other); got == WorkspaceID(real) {
		t.Fatalf("不同目录应不同键: %q", got)
	}
	// Clean 等价形（/./、//、尾分隔符）同键
	messy := link + string(filepath.Separator) + "." + string(filepath.Separator)
	if got := WorkspaceID(messy); got != WorkspaceID(real) {
		t.Fatalf("Clean 等价形应同键: %q vs %q", got, WorkspaceID(real))
	}
}

// TestWorkspaceIDTempAlias 系统级别名（macOS /tmp → /private/tmp）：EvalSymlinks
// 可消解时两写法同键（SPEC 九陷阱；Linux 等无此别名的平台跳过）。
func TestWorkspaceIDTempAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS 专属陷阱（/tmp → /private/tmp）")
	}
	resolved, err := filepath.EvalSymlinks("/tmp")
	if err != nil || resolved == "/tmp" {
		t.Skipf("本机 /tmp 非 symlink 别名（resolved=%q err=%v）", resolved, err)
	}
	if got, want := WorkspaceID("/tmp"), WorkspaceID(resolved); got != want {
		t.Fatalf("/tmp 别名应同键: got %q want %q", got, want)
	}
}

// TestResolveMissingDirFallback 目录不存在：EvalSymlinks 失败 → 回退 Clean+Abs 并
// 注记 fallback=true；Clean 等价形回退后同键（确定性不依赖拼写）。
func TestResolveMissingDirFallback(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "not-yet-created")
	canonical, fallback := Resolve(missing)
	if !fallback {
		t.Fatal("目录不存在应注记 fallback=true")
	}
	if want := filepath.Clean(missing); canonical != want {
		t.Fatalf("回退应为 Clean+Abs: got %q want %q", canonical, want)
	}
	// 等价拼写（经 .. 归位）回退后同键
	messy := filepath.Join(base, "sub", "..", "not-yet-created")
	c2, f2 := Resolve(messy)
	if !f2 || c2 != canonical {
		t.Fatalf("Clean 等价形应同键同注记: %q(f=%v) vs %q(f=%v)", c2, f2, canonical, fallback)
	}
	// WorkspaceID 对缺失目录仍输出稳定 16 位键（启动时目录允许尚未创建）
	if got := WorkspaceID(missing); len(got) != workspaceIDLen || got != WorkspaceID(messy) {
		t.Fatalf("缺失目录应得稳定键: %q", got)
	}
}

// TestResolveExistingDirNoFallback 存在目录：fallback=false 且消解 symlink。
// 注意：macOS 的 t.TempDir() 位于 /var/folders（含系统级 symlink /var→/private/var），
// 故断言为「确定性 + 别名归一」（/var/x 与 /private/var/x 必须同键），
// 而非「原样返回」——原样返回在含系统 symlink 的平台上不可满足。
func TestResolveExistingDirNoFallback(t *testing.T) {
	real := filepath.Join(t.TempDir(), "ws")
	if err := mkdir(real); err != nil {
		t.Fatal(err)
	}
	canonical, fallback := Resolve(real)
	if fallback {
		t.Fatal("存在目录不应回退")
	}
	again, fallback2 := Resolve(real)
	if fallback2 || canonical != again {
		t.Fatalf("同路径解析应确定: %q vs %q", canonical, again)
	}
	// 别名归一：若平台对路径前缀存在系统 symlink，两种写法必须同键
	if alt := filepath.Join(filepath.Dir(filepath.Dir(real)), filepath.Base(filepath.Dir(real)), "ws"); alt != real {
		if altCanon, fb := Resolve(alt); !fb && altCanon != canonical {
			t.Fatalf("别名路径应同键: %q vs %q", altCanon, canonical)
		}
	}
}

// mkdir / symlink 独立封装：失败信息带路径，测试主体保持简洁。
func mkdir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return &pathError{op: "mkdir", path: path, err: err}
	}
	return nil
}

func symlink(oldname, newname string) error {
	if err := os.Symlink(oldname, newname); err != nil {
		return &pathError{op: "symlink", path: newname, err: err}
	}
	return nil
}

// pathError 带路径的错误（仅测试报错可读性用）。
type pathError struct {
	op   string
	path string
	err  error
}

func (e *pathError) Error() string {
	return e.op + " " + e.path + ": " + e.err.Error()
}
