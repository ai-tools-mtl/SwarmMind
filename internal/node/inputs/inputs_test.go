package inputs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// ---------- 脚手架 ----------

// writeFile 写文件并返回内容 SHA-256（hex）。
func writeFile(t *testing.T, path, content string, perm os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// mustErr 检查 err 为 *Error 且文案以 input_prepare_failed 开头、包含全部 want 子串。
func mustErr(t *testing.T, err error, want ...string) *Error {
	t.Helper()
	if err == nil {
		t.Fatal("应失败")
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("应为 *Error, got %T: %v", err, err)
	}
	if !strings.HasPrefix(err.Error(), protocol.CodeInputPrepareFailed) {
		t.Fatalf("文案应以 %s 开头: %s", protocol.CodeInputPrepareFailed, err.Error())
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("文案应含 %q: %s", w, err.Error())
		}
	}
	return e
}

// noStaging 断言 inputs/ 下无暂存残留。
func noStaging(t *testing.T, workdir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(workdir, "inputs"))
	if err != nil {
		return // inputs/ 未创建也合法（零产物上游）
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagingPrefix) {
			t.Fatalf("暂存目录残留: %s", e.Name())
		}
	}
}

// fakeResolver idem → 上游解析结果（桩实现，隔离账本）。
type fakeResolver map[string]*UpstreamInputs

func (f fakeResolver) ResolveUpstream(_ context.Context, idem string) (*UpstreamInputs, error) {
	if up, ok := f[idem]; ok {
		return up, nil
	}
	return nil, &Error{IDem: idem, Reason: "本机账本无该 idem 的任务"}
}

// regUpstream 在上游 workdir 落盘文件并构造登记清单（Path=outputs/<id>/<rel>）。
func regUpstream(t *testing.T, workdir, taskID string, files map[string]string, perm os.FileMode) *UpstreamInputs {
	t.Helper()
	arts := make([]protocol.Artifact, 0, len(files))
	for rel, content := range files {
		p := filepath.Join(workdir, "outputs", taskID, filepath.FromSlash(rel))
		sum := writeFile(t, p, content, perm)
		arts = append(arts, protocol.Artifact{
			Path:   fmt.Sprintf("outputs/%s/%s", taskID, rel),
			Sha256: sum,
			Size:   int64(len(content)),
		})
	}
	return &UpstreamInputs{TaskID: taskID, Workdir: workdir, Artifacts: arts}
}

// ---------- Prepare/Mount ----------

// TestPrepareMountSuccess 挂载成功全链：多上游（含嵌套子目录与可执行位）纯复制
// 到 inputs/<上游task_id>/，内容逐字节一致、权限保留、暂存零残留、上游原样。
func TestPrepareMountSuccess(t *testing.T) {
	upW1, upW2, downW := t.TempDir(), t.TempDir(), t.TempDir()
	up1 := regUpstream(t, upW1, "t-up-1", map[string]string{
		"a.txt":       "hello upstream",
		"sub/b.txt":   "nested",
		"sub/c/d.bin": "\x00\x01binary",
	}, 0o644)
	up2 := regUpstream(t, upW2, "t-up-2", map[string]string{"run.sh": "#!/bin/sh\necho hi"}, 0o755)
	res := fakeResolver{"dep-a": up1, "dep-b": up2}

	if err := Prepare(context.Background(), res, downW, "t-down", []string{"dep-a", "dep-b"}); err != nil {
		t.Fatalf("挂载应成功: %v", err)
	}
	for _, c := range []struct{ rel, want string }{
		{filepath.Join("t-up-1", "a.txt"), "hello upstream"},
		{filepath.Join("t-up-1", "sub", "b.txt"), "nested"},
		{filepath.Join("t-up-1", "sub", "c", "d.bin"), "\x00\x01binary"},
		{filepath.Join("t-up-2", "run.sh"), "#!/bin/sh\necho hi"},
	} {
		b, err := os.ReadFile(filepath.Join(downW, "inputs", c.rel))
		if err != nil {
			t.Fatalf("读取挂载件 %s: %v", c.rel, err)
		}
		if string(b) != c.want {
			t.Fatalf("挂载件 %s 内容不符: %q", c.rel, b)
		}
	}
	if fi, err := os.Stat(filepath.Join(downW, "inputs", "t-up-2", "run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("可执行位应保留: %v %v", err, fi)
	}
	noStaging(t, downW)
	// 上游原样（纯复制非移动）
	if b, err := os.ReadFile(filepath.Join(upW1, "outputs", "t-up-1", "a.txt")); err != nil || string(b) != "hello upstream" {
		t.Fatalf("上游产物应原样保留: %v %q", err, b)
	}
}

// TestPrepareDownstreamEditNoPollution 红线：下游改动挂载件不得污染上游产物
// SHA（纯复制、非硬链接）。
func TestPrepareDownstreamEditNoPollution(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up", map[string]string{"a.txt": "original"}, 0o644)
	if err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"}); err != nil {
		t.Fatal(err)
	}
	mounted := filepath.Join(downW, "inputs", "t-up", "a.txt")
	if err := os.WriteFile(mounted, []byte("downstream tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(upW, "outputs", "t-up", "a.txt"))
	if err != nil || string(b) != "original" {
		t.Fatalf("上游产物被污染: %v %q", err, b)
	}
}

// TestPrepareSHAMismatch SHA 不符拒挂载：盘上内容与登记不一致 → 失败注明上游
// 与文件，零发布、暂存零残留。
func TestPrepareSHAMismatch(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up-1", map[string]string{"a.txt": "registered"}, 0o644)
	// 注册后盘上内容被改（等长篡改，绕过大小一致性先触发 SHA 口径不符）
	_ = writeFile(t, filepath.Join(upW, "outputs", "t-up-1", "a.txt"), "registerED", 0o644)
	err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	mustErr(t, err, "t-up-1", "a.txt", "SHA-256")
	if _, statErr := os.Lstat(filepath.Join(downW, "inputs", "t-up-1")); !os.IsNotExist(statErr) {
		t.Fatalf("失败不得发布输入目录: %v", statErr)
	}
	noStaging(t, downW)
}

// TestPrepareRollbackPublishFailure 原子发布失败回滚：第二上游发布失败 → 第一
// 上游已发布项撤销、被移开的旧目录归位、暂存全清理。
func TestPrepareRollbackPublishFailure(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up1 := regUpstream(t, upW, "t-up-1", map[string]string{"a.txt": "one"}, 0o644)
	up2 := regUpstream(t, upW, "t-up-2", map[string]string{"b.txt": "two"}, 0o644)
	// 预置旧目录（验证回滚归位）：inputs/t-up-1/old.txt
	oldDir := filepath.Join(downW, "inputs", "t-up-1")
	writeFile(t, filepath.Join(oldDir, "old.txt"), "stale", 0o644)

	orig := renameDir
	defer func() { renameDir = orig }()
	renameDir = func(src, dst string) error {
		if filepath.Base(dst) == "t-up-2" && filepath.Base(src) == "t-up-2" && !strings.Contains(src, "trash") {
			return errors.New("注入发布失败")
		}
		return os.Rename(src, dst)
	}
	err := Prepare(context.Background(), fakeResolver{"a": up1, "b": up2}, downW, "t-down", []string{"a", "b"})
	mustErr(t, err, "t-up-2", "发布输入目录失败")
	// 回滚：t-up-1 旧目录原样归位，t-up-2 未出现，暂存零残留
	if b, readErr := os.ReadFile(filepath.Join(oldDir, "old.txt")); readErr != nil || string(b) != "stale" {
		t.Fatalf("旧目录应归位: %v %q", readErr, b)
	}
	if _, statErr := os.Lstat(filepath.Join(downW, "inputs", "t-up-2")); !os.IsNotExist(statErr) {
		t.Fatal("失败上游不得发布")
	}
	noStaging(t, downW)
}

// TestPrepareRepublishExisting 幂等重发布：已存在同上游输入目录（恢复重挂/
// 同 agent 后续任务同依赖）→ 整目录替换，旧内容不残留。
func TestPrepareRepublishExisting(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up", map[string]string{"a.txt": "fresh"}, 0o644)
	writeFile(t, filepath.Join(downW, "inputs", "t-up", "stale.txt"), "old", 0o644)
	if err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(downW, "inputs", "t-up", "stale.txt")); !os.IsNotExist(err) {
		t.Fatal("旧内容应被替换")
	}
	if b, err := os.ReadFile(filepath.Join(downW, "inputs", "t-up", "a.txt")); err != nil || string(b) != "fresh" {
		t.Fatalf("新内容应就位: %v %q", err, b)
	}
	noStaging(t, downW)
}

// TestPrepareMissingArtifact 产物缺失（登记有、盘上无）：失败并注明缺失语义。
func TestPrepareMissingArtifact(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up", map[string]string{"a.txt": "x"}, 0o644)
	if err := os.Remove(filepath.Join(upW, "outputs", "t-up", "a.txt")); err != nil {
		t.Fatal(err)
	}
	err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	mustErr(t, err, "t-up", "a.txt", "缺失")
	noStaging(t, downW)
}

// TestPrepareTooManyArtifacts 超 8 件拒绝（对登记重检）。
func TestPrepareTooManyArtifacts(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	files := map[string]string{}
	for i := 0; i < protocol.MaxArtifacts+1; i++ {
		files[fmt.Sprintf("f%d.txt", i)] = fmt.Sprintf("content-%d", i)
	}
	up := regUpstream(t, upW, "t-up", files, 0o644)
	err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	mustErr(t, err, "t-up", fmt.Sprintf("登记 %d 件 > %d", protocol.MaxArtifacts+1, protocol.MaxArtifacts))
	noStaging(t, downW)
}

// TestPrepareOversize 单件超 640KiB 拒绝（登记超限与实得超限两个口径）。
func TestPrepareOversize(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	big := strings.Repeat("x", protocol.ArtifactLimit+1)
	up := &UpstreamInputs{TaskID: "t-up", Workdir: upW, Artifacts: []protocol.Artifact{{
		Path: "outputs/t-up/big.bin", Sha256: strings.Repeat("0", 64), Size: int64(len(big)),
	}}}
	err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	mustErr(t, err, "t-up", "超上限")
	noStaging(t, downW)

	// 盘上文件大于登记（登记合法、实得超限）→ 大小一致性校验拦截
	writeFile(t, filepath.Join(upW, "outputs", "t-up", "big.bin"), big, 0o644)
	up2 := &UpstreamInputs{TaskID: "t-up", Workdir: upW, Artifacts: []protocol.Artifact{{
		Path: "outputs/t-up/big.bin", Sha256: strings.Repeat("0", 64), Size: 1,
	}}}
	err = Prepare(context.Background(), fakeResolver{"d": up2}, downW, "t-down", []string{"d"})
	mustErr(t, err, "t-up", "大小不一致")
	noStaging(t, downW)
}

// TestPrepareSymlinkRejected 源为符号链接拒绝（不跟随，防越界）。
func TestPrepareSymlinkRejected(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	target := writeFile(t, filepath.Join(upW, "outside.txt"), "secret", 0o644)
	_ = target
	outDir := filepath.Join(upW, "outputs", "t-up")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(upW, "outside.txt"), filepath.Join(outDir, "a.txt")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	up := &UpstreamInputs{TaskID: "t-up", Workdir: upW, Artifacts: []protocol.Artifact{{
		Path: "outputs/t-up/a.txt", Sha256: strings.Repeat("0", 64), Size: 6,
	}}}
	err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	// 2026-10-24 复审 R1：末段链接改为内核 open 时拒绝（O_NOFOLLOW，ELOOP），
	// 早于 fd fstat——文案随之从「非常规文件」更新为「拒绝跟随」。
	mustErr(t, err, "t-up", "拒绝跟随")
	noStaging(t, downW)
}

// TestPreparePathTraversal 清单路径越出口径/穿越拒绝。
func TestPreparePathTraversal(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	for _, p := range []string{
		"outputs/t-other/a.txt",      // 非本上游口径
		"outputs/t-up/../../evil.txt", // 穿越
		"/etc/passwd",                 // 绝对路径
		"outputs/t-up/",               // 空相对路径
	} {
		up := &UpstreamInputs{TaskID: "t-up", Workdir: upW, Artifacts: []protocol.Artifact{{
			Path: p, Sha256: strings.Repeat("0", 64), Size: 1,
		}}}
		err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
		mustErr(t, err, "t-up", p)
	}
	noStaging(t, downW)
}

// TestPrepareZeroArtifactUpstream 答案型上游（completed 无产物登记）：不建目录、
// 不算失败。
func TestPrepareZeroArtifactUpstream(t *testing.T) {
	downW := t.TempDir()
	up := &UpstreamInputs{TaskID: "t-up", Workdir: t.TempDir(), Artifacts: nil}
	if err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"}); err != nil {
		t.Fatalf("零产物上游不应失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(downW, "inputs")); !os.IsNotExist(err) {
		t.Fatal("零产物不应创建 inputs 目录")
	}
}

// TestPrepareDedupSameUpstream 同一上游任务满足多个 idem：只挂载一次。
func TestPrepareDedupSameUpstream(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up", map[string]string{"a.txt": "x"}, 0o644)
	res := fakeResolver{"d1": up, "d2": up}
	if err := Prepare(context.Background(), res, downW, "t-down", []string{"d1", "d2"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(downW, "inputs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "t-up" {
		t.Fatalf("应只挂载一次 t-up, got %v", entries)
	}
}

// TestPrepareTimeout 独立超时：ctx 已到期 → 超时失败、暂存清理、零发布。
func TestPrepareTimeout(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up", map[string]string{"a.txt": "x"}, 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	err := Prepare(ctx, fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	mustErr(t, err, "超时")
	noStaging(t, downW)
}

// errResolver 解析器恒返回非 *Error 的裸错误（验证 Prepare 的包装防御）。
type errResolver struct{}

func (errResolver) ResolveUpstream(context.Context, string) (*UpstreamInputs, error) {
	return nil, errors.New("boom")
}

// TestPrepareResolverError 包装解析失败：非 *Error 错误补 idem 与码前缀。
func TestPrepareResolverError(t *testing.T) {
	downW := t.TempDir()
	err := Prepare(context.Background(), errResolver{}, downW, "t-down", []string{"d"})
	mustErr(t, err, "d", "boom")
}

// ---------- LocalResolver ----------

// ledgerTask 向账本登记一条任务。
func ledgerTask(t *testing.T, led *ledger.Ledger, id, idem, agent string, at time.Time,
	status string, arts []protocol.Artifact) {
	t.Helper()
	task := protocol.Task{ID: id, TraceID: "tr", TargetNode: "n1", TargetAgent: agent,
		SubmittedBy: "hub", IdemKey: idem, CreatedAt: at, UpdatedAt: at}
	if err := led.Accept(task); err != nil {
		t.Fatal(err)
	}
	if status == "" {
		return // 保持 accepted（非终态占位，模拟在途上游）
	}
	res := protocol.TaskResult{Status: status, ExitCode: 0, Artifacts: arts, FinishedAt: at}
	if err := led.MarkFinished(id, res); err != nil {
		t.Fatal(err)
	}
}

func artifactsOf(t *testing.T, workdir, taskID string, files map[string]string) []protocol.Artifact {
	t.Helper()
	arts := make([]protocol.Artifact, 0, len(files))
	for rel, content := range files {
		p := filepath.Join(workdir, "outputs", taskID, filepath.FromSlash(rel))
		sum := writeFile(t, p, content, 0o644)
		arts = append(arts, protocol.Artifact{
			Path: fmt.Sprintf("outputs/%s/%s", taskID, rel), Sha256: sum, Size: int64(len(content)),
		})
	}
	return arts
}

// TestLocalResolverEarliestCompleted 多版本同 idem 取最早 completed（对齐 hub
// resolveIDem 口径）。
func TestLocalResolverEarliestCompleted(t *testing.T) {
	upW := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	base := protocol.NowUTC()
	arts2 := artifactsOf(t, upW, "t-v2", map[string]string{"a.txt": "v2"})
	ledgerTask(t, led, "t-v2", "dep", "agent-a", base, protocol.ResultCompleted, arts2)
	arts1 := artifactsOf(t, upW, "t-v1", map[string]string{"a.txt": "v1"})
	ledgerTask(t, led, "t-v1", "dep", "agent-a", base.Add(-time.Minute), protocol.ResultCompleted, arts1)

	r := &LocalResolver{Ledger: led, RootOf: func(string) (string, bool) { return upW, true }}
	up, err := r.ResolveUpstream(context.Background(), "dep")
	if err != nil {
		t.Fatal(err)
	}
	if up.TaskID != "t-v1" || len(up.Artifacts) != 1 || up.Artifacts[0].Sha256 != arts1[0].Sha256 {
		t.Fatalf("应取最早 completed t-v1: %+v", up)
	}
	if up.Workdir != upW {
		t.Fatalf("workdir 应来自 RootOf: %q", up.Workdir)
	}
}

// TestLocalResolverFailures 解析失败语义：无条目/无 completed/agent 根缺失。
func TestLocalResolverFailures(t *testing.T) {
	upW := t.TempDir()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	base := protocol.NowUTC()
	ledgerTask(t, led, "t-f", "dep-failed", "agent-a", base, protocol.ResultFailed, nil)
	ledgerTask(t, led, "t-live", "dep-live", "agent-a", base, "", nil) // 恢复路径占位：accepted

	r := &LocalResolver{Ledger: led, RootOf: func(string) (string, bool) { return upW, true }}
	for _, c := range []struct{ idem, want string }{
		{"dep-unknown", "无该 idem 的任务"},
		{"dep-failed", "completed 上游版本"},
		{"dep-live", "尚未完成"},
	} {
		_, err := r.ResolveUpstream(context.Background(), c.idem)
		mustErr(t, err, c.idem, c.want)
	}

	// completed 零产物：返回空清单不失败
	ledgerTask(t, led, "t-noart", "dep-noart", "agent-a", base, protocol.ResultCompleted, nil)
	up, err := r.ResolveUpstream(context.Background(), "dep-noart")
	if err != nil {
		t.Fatal(err)
	}
	if up.TaskID != "t-noart" || len(up.Artifacts) != 0 {
		t.Fatalf("零产物上游应返回空清单: %+v", up)
	}

	// agent 根缺失（配置漂移）
	r2 := &LocalResolver{Ledger: led, RootOf: func(string) (string, bool) { return "", false }}
	ledgerTask(t, led, "t-v1b", "dep-root", "agent-a", base, protocol.ResultCompleted,
		artifactsOf(t, upW, "t-v1b", map[string]string{"a.txt": "x"}))
	_, err = r2.ResolveUpstream(context.Background(), "dep-root")
	mustErr(t, err, "t-v1b", "产物根不可定位")
}

// ---------- 文件操作安全加固（codex 审查 2026-10-24） ----------

// TestPrepareInputsSymlinkRejected inputs 基目录为符号链接拒绝（防 MkdirAll
// 跟随链接逃逸 workdir）：链接目标目录零触碰。
func TestPrepareInputsSymlinkRejected(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up", map[string]string{"a.txt": "x"}, 0o644)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(downW, "inputs")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	mustErr(t, err, "输入区基目录", "符号链接")
	// 链接目标零触碰：外部目录仍为空
	ents, readErr := os.ReadDir(outside)
	if readErr != nil || len(ents) != 0 {
		t.Fatalf("符号链接目标不得被写入: %v %v", readErr, ents)
	}
}

// TestPrepareParentSymlinkRejected 源路径父目录（outputs/<task_id>）为符号链接
// 拒绝：整条路径逐段 Lstat，中间段链接不跟随。
func TestPrepareParentSymlinkRejected(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	realDir := filepath.Join(upW, "elsewhere")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(realDir, "a.txt"), "x", 0o644)
	outDir := filepath.Join(upW, "outputs", "t-up")
	if err := os.MkdirAll(filepath.Dir(outDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, outDir); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	up := &UpstreamInputs{TaskID: "t-up", Workdir: upW, Artifacts: []protocol.Artifact{{
		Path: "outputs/t-up/a.txt", Sha256: strings.Repeat("0", 64), Size: 1,
	}}}
	err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	mustErr(t, err, "t-up", "a.txt", "符号链接")
	noStaging(t, downW)
}

// TestCopyStreamCancelMidCopy 复制中途取消：ctx 中断后复制立即收敛并返回 ctx
// 错误，已复制字节数如实返回（部分计数）。
func TestCopyStreamCancelMidCopy(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	go func() { // 持续写入（远大于管道缓冲），复制必处于中途
		defer pw.Close()
		buf := make([]byte, 4096)
		for {
			if _, werr := pw.Write(buf); werr != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	n, cerr := copyStream(ctx, io.Discard, pr)
	if !isCtxErr(cerr) {
		t.Fatalf("应返回 ctx 错误, got %v", cerr)
	}
	if n == 0 {
		t.Fatal("取消前应有部分复制进度")
	}
}

// TestPrepareRollbackRestoreFailureKeepsMaterials 回滚归位失败保留材料：撤销
// 已发布成功但旧目录归位失败 → 错误聚合报告（归位失败+人工恢复+材料路径），
// 暂存/trash/journal 全部保留（重挂时 recoverStaging 按实物收敛或人工恢复）。
func TestPrepareRollbackRestoreFailureKeepsMaterials(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up1 := regUpstream(t, upW, "t-up-1", map[string]string{"a.txt": "one"}, 0o644)
	up2 := regUpstream(t, upW, "t-up-2", map[string]string{"b.txt": "two"}, 0o644)
	oldDir := filepath.Join(downW, "inputs", "t-up-1")
	writeFile(t, filepath.Join(oldDir, "old.txt"), "stale", 0o644)

	orig := renameDir
	defer func() { renameDir = orig }()
	renameDir = func(src, dst string) error {
		if strings.Contains(src, "/trash/") {
			return errors.New("注入归位失败") // 旧目录归位（回滚步骤）必炸
		}
		if filepath.Base(src) == "t-up-2" && filepath.Base(dst) == "t-up-2" {
			return errors.New("注入发布失败") // 触发回滚
		}
		return os.Rename(src, dst)
	}
	err := Prepare(context.Background(), fakeResolver{"a": up1, "b": up2}, downW, "t-down", []string{"a", "b"})
	e := mustErr(t, err, "t-up-2", "发布输入目录失败", "归位失败", "人工恢复", "t-up-1", "材料保留")
	if !strings.Contains(e.Reason, "回滚未完全") {
		t.Fatalf("应聚合回滚未完全标注: %q", e.Reason)
	}
	// 材料保留：暂存目录（含 journal 与 trash）仍在，旧目录滞留 trash 未丢失
	leftovers, readErr := filepath.Glob(filepath.Join(downW, "inputs", stagingPrefix+"t-down-*"))
	if readErr != nil || len(leftovers) != 1 {
		t.Fatalf("暂存材料应保留: %v %v", readErr, leftovers)
	}
	stage := leftovers[0]
	if _, statErr := os.Stat(filepath.Join(stage, "journal.json")); statErr != nil {
		t.Fatalf("journal 应随材料保留: %v", statErr)
	}
	if b, readErr := os.ReadFile(filepath.Join(stage, "trash", "t-up-1", "old.txt")); readErr != nil || string(b) != "stale" {
		t.Fatalf("旧目录应滞留 trash（材料不删）: %v %q", readErr, b)
	}
	if _, statErr := os.Lstat(oldDir); !os.IsNotExist(statErr) {
		t.Fatal("归位失败时旧目录不得在输入区（仍在 trash）")
	}
}

// craftCrashStage 在 inputs/ 下伪造一个崩溃残留的暂存目录（含 trash 与可选暂存
// 内容），返回其相对路径（slash 口径）。
func craftCrashStage(t *testing.T, workdir, taskID string, j *journal, staged map[string]string) string {
	t.Helper()
	stageRel := inputsRel + "/" + stagingPrefix + taskID + "-cafebabe"
	stageAbs := filepath.Join(workdir, filepath.FromSlash(stageRel))
	if err := os.MkdirAll(filepath.Join(stageAbs, "trash"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range staged {
		writeFile(t, filepath.Join(stageAbs, filepath.FromSlash(rel)), content, 0o644)
	}
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageAbs, journalName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return stageRel
}

// TestRecoverJournalCompletesPublish 崩溃恢复·redo：journal 记录 old_moved、
// 暂存内容在（崩溃于「移开旧」与「发布新」之间）→ 重挂先完成未竟发布。以
// 上游篡改使后续暂存必失败，隔离验证恢复动作的盘上效果。
func TestRecoverJournalCompletesPublish(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up", map[string]string{"a.txt": "new"}, 0o644)
	craftCrashStage(t, downW, "t-down", &journal{TaskID: "t-down", Steps: []journalStep{
		{Upstream: "t-up", State: stepOldMoved},
	}}, map[string]string{"t-up/a.txt": "new"})
	_ = writeFile(t, filepath.Join(stageTrashOld(t, downW, "t-down"), "old.txt"), "old", 0o644)
	// 上游登记后等长篡改（专触发 SHA 口径）：重挂暂存必失败，恢复效果不受后续挂载覆盖
	_ = writeFile(t, filepath.Join(upW, "outputs", "t-up", "a.txt"), "nEw", 0o644)

	err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	mustErr(t, err, "SHA-256")
	// 恢复已完成发布：暂存内容（登记一致版）就位输入区，崩溃残留全清理
	if b, readErr := os.ReadFile(filepath.Join(downW, "inputs", "t-up", "a.txt")); readErr != nil || string(b) != "new" {
		t.Fatalf("恢复应完成未竟发布: %v %q", readErr, b)
	}
	noStaging(t, downW)
}

// stageTrashOld 取伪造暂存目录的 trash/<taskID> 绝对路径（测试落盘用）。
func stageTrashOld(t *testing.T, workdir, taskID string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(workdir, "inputs", stagingPrefix+taskID+"-*", "trash"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("定位伪造 trash 失败: %v %v", err, matches)
	}
	return filepath.Join(matches[0], "t-up")
}

// TestRecoverJournalRestoresOld 崩溃恢复·undo：journal 记录 old_moved 但暂存
// 内容缺失（异常窗口）→ 旧目录自 trash 归位，输入区回到挂载前原状。
func TestRecoverJournalRestoresOld(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up", map[string]string{"a.txt": "new"}, 0o644)
	craftCrashStage(t, downW, "t-down", &journal{TaskID: "t-down", Steps: []journalStep{
		{Upstream: "t-up", State: stepOldMoved},
	}}, nil) // 无暂存内容
	_ = writeFile(t, filepath.Join(stageTrashOld(t, downW, "t-down"), "old.txt"), "old", 0o644)
	// 上游产物删除：重挂暂存必失败，隔离验证恢复动作
	if err := os.Remove(filepath.Join(upW, "outputs", "t-up", "a.txt")); err != nil {
		t.Fatal(err)
	}

	err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"})
	mustErr(t, err, "缺失")
	if b, readErr := os.ReadFile(filepath.Join(downW, "inputs", "t-up", "old.txt")); readErr != nil || string(b) != "old" {
		t.Fatalf("旧目录应自 trash 归位: %v %q", readErr, b)
	}
	noStaging(t, downW)
}

// TestRecoverJournalNoResidue 崩溃恢复·收敛清理：无 journal（发布阶段前崩溃）
// 与全 published 两类残留，重挂后暂存零残留、挂载结果正确。
func TestRecoverJournalNoResidue(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up := regUpstream(t, upW, "t-up", map[string]string{"a.txt": "x"}, 0o644)

	// 无 journal：发布阶段前崩溃，输入区未被触碰，直接清理
	craftCrashStage(t, downW, "t-down", nil, map[string]string{"t-up/a.txt": "junk"})
	if err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"}); err != nil {
		t.Fatalf("无 journal 残留不应阻塞挂载: %v", err)
	}
	if b, readErr := os.ReadFile(filepath.Join(downW, "inputs", "t-up", "a.txt")); readErr != nil || string(b) != "x" {
		t.Fatalf("挂载应正常完成: %v %q", readErr, b)
	}
	noStaging(t, downW)

	// 全 published：发布已完成仅清理中断，残留清走、已发布内容不动
	_ = os.RemoveAll(filepath.Join(downW, "inputs", "t-up"))
	craftCrashStage(t, downW, "t-down", &journal{TaskID: "t-down", Steps: []journalStep{
		{Upstream: "t-up", State: stepPublished},
	}}, nil)
	if err := Prepare(context.Background(), fakeResolver{"d": up}, downW, "t-down", []string{"d"}); err != nil {
		t.Fatalf("published 残留不应阻塞挂载: %v", err)
	}
	noStaging(t, downW)
}

// TestPrepareCancelAfterPublishRollsBack 末次发布后复查：发布中途 ctx 被取消
// → Prepare 必返回失败（调用方不得 MarkStarted），输入区回滚到挂载前原状。
func TestPrepareCancelAfterPublishRollsBack(t *testing.T) {
	upW, downW := t.TempDir(), t.TempDir()
	up1 := regUpstream(t, upW, "t-up-1", map[string]string{"a.txt": "one"}, 0o644)
	up2 := regUpstream(t, upW, "t-up-2", map[string]string{"b.txt": "two"}, 0o644)
	oldDir := filepath.Join(downW, "inputs", "t-up-1")
	writeFile(t, filepath.Join(oldDir, "old.txt"), "stale", 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orig := renameDir
	defer func() { renameDir = orig }()
	renameDir = func(src, dst string) error {
		err := os.Rename(src, dst)
		// 第一个上游发布成功即取消（模拟发布窗口内管理面中止）
		if err == nil && filepath.Base(dst) == "t-up-1" &&
			filepath.Base(filepath.Dir(dst)) == inputsRel {
			cancel()
		}
		return err
	}
	err := Prepare(ctx, fakeResolver{"a": up1, "b": up2}, downW, "t-down", []string{"a", "b"})
	mustErr(t, err, "中止")
	// 输入区回滚到挂载前原状：旧目录归位、新目录未出现、暂存零残留
	if b, readErr := os.ReadFile(filepath.Join(oldDir, "old.txt")); readErr != nil || string(b) != "stale" {
		t.Fatalf("旧目录应归位: %v %q", readErr, b)
	}
	if _, statErr := os.Lstat(filepath.Join(downW, "inputs", "t-up-2")); !os.IsNotExist(statErr) {
		t.Fatal("取消后不得留下已发布新目录")
	}
	noStaging(t, downW)
}
