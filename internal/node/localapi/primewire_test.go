package localapi

// declare/rotate 统筹自动接线单测（t20261023-62a2）：zcode 布局命中合并、幂等
// 重复写入、备份生成与覆盖、坏 JSON/目录不存在降级不硬写、rotate 后 token 刷新
// 进配置、ConfigHome 为空兜底 data_dir/home——测试 token 全为假值（纪律：真
// token 不进代码库）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// writeZcodeSetting 在 home 内落 zcode 宿主布局（.zcode/cli/setting.json），
// 返回文件路径。
func writeZcodeSetting(t *testing.T, home, content string) string {
	t.Helper()
	p := filepath.Join(home, ".zcode", "cli", "setting.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// readSettingDoc 读回 setting.json 为 map（数值开 UseNumber，保字面量断言）。
func readSettingDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("接线后 setting.json 必须可解析: %v\n%s", err, b)
	}
	return doc
}

// serversOf 取 doc.mcpServers（缺失 Fatal）。
func serversOf(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers 缺失或非对象: %v", doc["mcpServers"])
	}
	return servers
}

// entryToken 取 mcpServers.swarmmind.env.SWARM_NODE_PRIME_TOKEN（缺任一层 Fatal）。
func entryToken(t *testing.T, doc map[string]any) string {
	t.Helper()
	entry, ok := serversOf(t, doc)["swarmmind"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers.swarmmind 缺失")
	}
	env, ok := entry["env"].(map[string]any)
	if !ok {
		t.Fatalf("swarmmind.env 缺失: %v", entry)
	}
	tok, _ := env["SWARM_NODE_PRIME_TOKEN"].(string)
	return tok
}

// fakeMCP 假 token 的 MCP 片段（与 mcpConfig 同构）。
func fakeMCP(tok string) map[string]any {
	return map[string]any{
		"command": "/fake/swarmmind-mcp",
		"env":     map[string]any{"SWARM_NODE_PRIME_TOKEN": tok},
	}
}

// ---------- wirePrimeConfig 直测（布局探测/合并/备份/降级） ----------

// TestWirePrimeConfigZcodeMerge zcode 布局命中：合并 mcpServers.swarmmind、
// 其他键与其他 MCP 条目原样、无关键数值字面量不经 float 换算、0600 原子写、
// target 为绝对路径。
func TestWirePrimeConfigZcodeMerge(t *testing.T) {
	home := t.TempDir()
	// 9007199254740993 = 2^53+1：超 float64 安全整数——一旦解码丢 UseNumber 走
	// float64 往返，会换算成 9007199254740992（R5：原 3000000000 验证不了精度）。
	const big = "9007199254740993"
	const raw = `{"theme":"dark","port":8445,"big":` + big + `,"mcpServers":{"other":{"command":"keep-me"}}}`
	p := writeZcodeSetting(t, home, raw)

	wired, target, err := wirePrimeConfig(home, fakeMCP("tok-fake-1"))
	if err != nil || !wired {
		t.Fatalf("zcode 布局应命中: wired=%v err=%v", wired, err)
	}
	if target != p || !filepath.IsAbs(target) {
		t.Fatalf("target 应为 setting.json 绝对路径, got %q", target)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), big) {
		t.Fatalf("无关键大整数字面量必须原样保留（不得 float64 换算）:\n%s", b)
	}
	if strings.Contains(string(b), "9007199254740992") {
		t.Fatalf("大整数被 float64 往返换算成 2^53（UseNumber 语义被破坏）:\n%s", b)
	}
	doc := readSettingDoc(t, p)
	if tok := entryToken(t, doc); tok != "tok-fake-1" {
		t.Fatalf("swarmmind.env token=%q, want tok-fake-1", tok)
	}
	if doc["theme"] != "dark" {
		t.Fatalf("无关键 theme 应保留: %v", doc["theme"])
	}
	if other := serversOf(t, doc)["other"].(map[string]any); other["command"] != "keep-me" {
		t.Fatalf("其他 MCP 条目应原样: %v", other)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("setting.json 应 0600（内含 prime_token 凭证）, got %v", fi.Mode().Perm())
	}
}

// TestWirePrimeConfigMergeNoServersKey mcpServers 键不存在：新建而不动其他
// 顶层键。
func TestWirePrimeConfigMergeNoServersKey(t *testing.T) {
	home := t.TempDir()
	p := writeZcodeSetting(t, home, `{"theme":"dark"}`)
	wired, _, err := wirePrimeConfig(home, fakeMCP("tok-fake-1"))
	if err != nil || !wired {
		t.Fatalf("无 mcpServers 键应新建命中: wired=%v err=%v", wired, err)
	}
	doc := readSettingDoc(t, p)
	if tok := entryToken(t, doc); tok != "tok-fake-1" {
		t.Fatalf("token=%q, want tok-fake-1", tok)
	}
	if doc["theme"] != "dark" {
		t.Fatalf("无关键 theme 应保留: %v", doc["theme"])
	}
}

// TestWirePrimeConfigIdempotent 幂等重复写入：同键覆盖（不重复、不报错），
// 撤销/重钦定反复 declare 同口径。
func TestWirePrimeConfigIdempotent(t *testing.T) {
	home := t.TempDir()
	p := writeZcodeSetting(t, home, `{"mcpServers":{"other":{"command":"keep"}}}`)
	for i, tok := range []string{"tok-fake-a", "tok-fake-a", "tok-fake-b"} {
		wired, _, err := wirePrimeConfig(home, fakeMCP(tok))
		if err != nil || !wired {
			t.Fatalf("第 %d 次写入应成功: wired=%v err=%v", i+1, wired, err)
		}
		doc := readSettingDoc(t, p)
		if got := entryToken(t, doc); got != tok {
			t.Fatalf("第 %d 次写入后 token=%q, want %q（同键覆盖）", i+1, got, tok)
		}
		if len(serversOf(t, doc)) != 2 {
			t.Fatalf("mcpServers 应恰 other+swarmmind 两键（不堆重复条目）: %v", serversOf(t, doc))
		}
		if other := serversOf(t, doc)["other"].(map[string]any); other["command"] != "keep" {
			t.Fatalf("其他 MCP 条目不得被挤掉: %v", other)
		}
	}
}

// TestWirePrimeConfigBackup 备份生成与覆盖：写前备份 .bak-prime 恰一个，
// 内容是上一次写前的原文（旧备份直接覆盖，不堆积）。
func TestWirePrimeConfigBackup(t *testing.T) {
	home := t.TempDir()
	const orig = `{"theme":"dark"}`
	p := writeZcodeSetting(t, home, orig)

	if _, _, err := wirePrimeConfig(home, fakeMCP("tok-fake-1")); err != nil {
		t.Fatal(err)
	}
	bak := p + primeBackupSuffix
	b, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("首次接线应生成备份: %v", err)
	}
	if string(b) != orig {
		t.Fatalf("首次备份应为原文:\n got %s\nwant %s", b, orig)
	}
	// 第二次（rotate 场景）：备份被覆盖为「第二次写前」的内容（含旧 token 条目）
	after1, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := wirePrimeConfig(home, fakeMCP("tok-fake-2")); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(after1) {
		t.Fatalf("二次接线备份应覆盖为上一次写前内容（不堆积）:\n got %s\nwant %s", b, after1)
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-prime") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("备份文件应恰一个, got %d", n)
	}
}

// TestWirePrimeConfigBadJSONDegrade 坏 JSON 降级：wired=false、err 带说明、
// 原文件一个字节不动、不生成备份。
func TestWirePrimeConfigBadJSONDegrade(t *testing.T) {
	home := t.TempDir()
	const bad = `{"mcpServers": oops-not-json`
	p := writeZcodeSetting(t, home, bad)
	wired, target, err := wirePrimeConfig(home, fakeMCP("tok-fake-1"))
	if wired || err == nil {
		t.Fatalf("坏 JSON 应降级且 err 带说明: wired=%v err=%v", wired, err)
	}
	if target != "" {
		t.Fatalf("降级时 target 应为空, got %q", target)
	}
	b, err2 := os.ReadFile(p)
	if err2 != nil || string(b) != bad {
		t.Fatalf("坏 JSON 降级不得动原文件: err=%v\n got %s\nwant %s", err2, b, bad)
	}
	if _, err2 := os.Stat(p + primeBackupSuffix); !os.IsNotExist(err2) {
		t.Fatal("坏 JSON 降级不得生成备份")
	}
}

// TestWirePrimeConfigNonObjectDegrade 顶层非对象 / 顶层 null / mcpServers 非
// 对象降级不硬写（猜不出安全合并点，宁可手贴）。null 单列（R1）：Decode 成功
// 但 doc 置 nil——修复前此处 panic。
func TestWirePrimeConfigNonObjectDegrade(t *testing.T) {
	for name, content := range map[string]string{
		"顶层非对象":         `[1,2,3]`,
		"顶层null":        `null`,
		"mcpServers非对象": `{"mcpServers":"oops"}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			p := writeZcodeSetting(t, home, content)
			wired, _, err := wirePrimeConfig(home, fakeMCP("tok-fake-1"))
			if wired || err == nil {
				t.Fatalf("非对象布局应降级: wired=%v err=%v", wired, err)
			}
			b, err2 := os.ReadFile(p)
			if err2 != nil || string(b) != content {
				t.Fatalf("降级不得动原文件:\n got %s\nwant %s", b, content)
			}
		})
	}
}

// TestWirePrimeConfigTrailingGarbageDegrade 尾部垃圾降级（R2）：首值之后仅容
// 空白——`{} garbage`/`{} {}` 这类「先解出对象再带尾巴」的文件不是合法配置，
// 不得备份覆盖原文。
func TestWirePrimeConfigTrailingGarbageDegrade(t *testing.T) {
	for name, content := range map[string]string{
		"对象后垃圾文本":  `{} garbage`,
		"两个对象":     `{} {}`,
		"对象后换行再跟值": "{\"a\":1}\n2",
		"仅尾随空白应命中": "{\"theme\":\"dark\"} \n\t",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			p := writeZcodeSetting(t, home, content)
			wired, _, err := wirePrimeConfig(home, fakeMCP("tok-fake-1"))
			if name == "仅尾随空白应命中" {
				// 对照组：合法 JSON 后只有空白 → 正常命中（不被 R2 误伤）
				if err != nil || !wired {
					t.Fatalf("尾部仅空白应命中: wired=%v err=%v", wired, err)
				}
				return
			}
			if wired || err == nil {
				t.Fatalf("尾部垃圾应降级且 err 带说明: wired=%v err=%v", wired, err)
			}
			b, err2 := os.ReadFile(p)
			if err2 != nil || string(b) != content {
				t.Fatalf("降级不得动原文件:\n got %s\nwant %s", b, content)
			}
			if _, err2 := os.Stat(p + primeBackupSuffix); !os.IsNotExist(err2) {
				t.Fatal("尾部垃圾降级不得生成备份")
			}
		})
	}
}

// TestWirePrimeConfigWriteFailureDegrade 备份/rename 失败降级（R5）：写盘链路
// 任一步失败 → wired=false、err 带说明、主文件逐字节不动、不留临时文件。
func TestWirePrimeConfigWriteFailureDegrade(t *testing.T) {
	t.Run("备份写失败-目录不可写", func(t *testing.T) {
		home := t.TempDir()
		const orig = `{"theme":"dark"}`
		p := writeZcodeSetting(t, home, orig)
		cliDir := filepath.Dir(p)
		if err := os.Chmod(cliDir, 0o500); err != nil { // 只读目录：临时文件创建即失败
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cliDir, 0o755) }) // 还原权限，TempDir 清理才能删
		wired, target, err := wirePrimeConfig(home, fakeMCP("tok-fake-1"))
		if wired || err == nil {
			t.Fatalf("备份写失败应降级: wired=%v err=%v", wired, err)
		}
		if target != "" {
			t.Fatalf("降级时 target 应为空, got %q", target)
		}
		if b, err2 := os.ReadFile(p); err2 != nil || string(b) != orig {
			t.Fatalf("降级不得动主文件:\n got %s\nwant %s", b, orig)
		}
	})
	t.Run("备份rename失败-备份位被目录占位", func(t *testing.T) {
		home := t.TempDir()
		const orig = `{"theme":"dark"}`
		p := writeZcodeSetting(t, home, orig)
		// .bak-prime 已是目录：临时文件能建，rename 文件→目录必失败（EISDIR）
		if err := os.Mkdir(p+primeBackupSuffix, 0o755); err != nil {
			t.Fatal(err)
		}
		wired, _, err := wirePrimeConfig(home, fakeMCP("tok-fake-1"))
		if wired || err == nil || !strings.Contains(err.Error(), "备份") {
			t.Fatalf("备份 rename 失败应降级且 err 指明备份环节: wired=%v err=%v", wired, err)
		}
		if b, err2 := os.ReadFile(p); err2 != nil || string(b) != orig {
			t.Fatalf("降级不得动主文件:\n got %s\nwant %s", b, orig)
		}
		entries, err2 := os.ReadDir(filepath.Dir(p))
		if err2 != nil {
			t.Fatal(err2)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp-") {
				t.Fatalf("rename 失败后不得残留临时文件: %s", e.Name())
			}
		}
	})
}

// TestWirePrimeConfigMissingHomeDegrade 目录不存在降级：wired=false、err=nil
//（降级不是错误）、绝不创建宿主目录；空 home 同口径降级（带说明）。
func TestWirePrimeConfigMissingHomeDegrade(t *testing.T) {
	home := filepath.Join(t.TempDir(), "no", "such", "home")
	wired, target, err := wirePrimeConfig(home, fakeMCP("tok-fake-1"))
	if wired || err != nil || target != "" {
		t.Fatalf("目录不存在应静默降级: wired=%v err=%v target=%q", wired, err, target)
	}
	if _, serr := os.Stat(home); !os.IsNotExist(serr) {
		t.Fatalf("降级绝不创建宿主目录: %s 不应存在", home)
	}
	wired, _, err = wirePrimeConfig("", fakeMCP("tok-fake-1"))
	if wired || err == nil {
		t.Fatalf("空 home 应降级带说明: wired=%v err=%v", wired, err)
	}
}

// ---------- declare/rotate 端到端（影子 home 接线口径） ----------

// newWireServer 自动接线端到端环境：echo 的影子 home 为 echoHome（空 =
// ConfigHome 未配置，走 data_dir/home 兜底——dataDir 同时是 node 数据目录）。
// 返回服务与影子 home 内 setting.json 的预期路径。command 探测默认注桩命中
// （R4 后接线要求非占位符；占位符降级语义由专测覆盖）。
func newWireServer(t *testing.T, echoHome, dataDir string) (*httptest.Server, string) {
	return newWireServerCmd(t, echoHome, dataDir, "/fake/swarmmind-mcp")
}

// newWireServerCmd newWireServer 的底层形态；cmdOverride 为空 = 不注桩，走
// mcpCommand 真实探测（测试环境必然占位符——R4 降级路径专用）。
func newWireServerCmd(t *testing.T, echoHome, dataDir, cmdOverride string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(cfgPath, []byte(declareYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	svc := svcpkg.New(svcpkg.Config{
		Ledger:  led,
		Runners: map[string]svcpkg.Runner{"echo": echoStubRunner{}},
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	echo := echoProfileLocal(t)
	echo.ConfigHome = echoHome
	n := 0
	s := New(Config{
		NodeID: "nodeA", Service: svc, Ledger: led,
		Agents: newState(t, dir, []protocol.ExecProfile{echo,
			{Name: "sleeper", Kind: protocol.AgentKindCLI, Command: "/bin/sleep",
				Workdir: t.TempDir(), TimeoutSec: 1, Exposed: false}}),
		LocalToken:        "tok-local",
		PrimeToken:        "tok-old",
		LocalListen:       "127.0.0.1:8445",
		DeclareConfigPath: cfgPath,
		DataDir:           dataDir,
		RotatePrime: func() (string, error) {
			n++
			return fmt.Sprintf("tok-wire-%d", n), nil
		},
	})
	s.mcpCommandOverride = cmdOverride
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	home := echoHome
	if home == "" && dataDir != "" {
		home = filepath.Join(dataDir, "home")
	}
	return srv, filepath.Join(home, ".zcode", "cli", "setting.json")
}

// wireResp declare/rotate 应答公共口径（wired 字段）。
type wireResp struct {
	PrimeToken  string         `json:"prime_token"`
	MCPConfig   map[string]any `json:"mcp_config"`
	Wired       bool           `json:"wired"`
	WiredTarget string         `json:"wired_target"`
	Note        string         `json:"note"`
}

// doDeclare 调 declare 并解析应答。
func doDeclare(t *testing.T, srv *httptest.Server, agent string) (*wireResp, int) {
	t.Helper()
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/declare",
		`{"prime_agent":"`+agent+`"}`)
	var resp wireResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("declare 应答解析失败: %v\n%s", err, out)
	}
	return &resp, code
}

// TestPrimeDeclareWiresShadowHome 钦定即接线：影子 home 命中 zcode 布局 →
// 应答 wired=true + wired_target 绝对路径 + note 告知已写入路径 + mcp_config
// 仍在；文件内 token 与应答一致、其他 MCP 条目保留。
func TestPrimeDeclareWiresShadowHome(t *testing.T) {
	home := t.TempDir()
	setting := writeZcodeSetting(t, home, `{"mcpServers":{"other":{"command":"keep"}}}`)
	srv, wantPath := newWireServer(t, home, "")
	if wantPath != setting {
		t.Fatalf("测试布局路径不符: got %q want %q", wantPath, setting)
	}
	resp, code := doDeclare(t, srv, "echo")
	if code != http.StatusOK {
		t.Fatalf("declare 应 200, got %d", code)
	}
	if !resp.Wired || resp.WiredTarget != setting || !filepath.IsAbs(resp.WiredTarget) {
		t.Fatalf("应 wired=true 且 wired_target 为 setting.json 绝对路径: %+v", resp)
	}
	if !strings.Contains(resp.Note, setting) {
		t.Fatalf("note 应告知已自动写入的路径: %s", resp.Note)
	}
	if resp.MCPConfig == nil {
		t.Fatalf("mcp_config 片段应保留（降级兜底与手工迁移口径）: %+v", resp)
	}
	doc := readSettingDoc(t, setting)
	if tok := entryToken(t, doc); tok != resp.PrimeToken || tok != "tok-wire-1" {
		t.Fatalf("文件内 token=%q 应等于应答新凭证 tok-wire-1", tok)
	}
	entry, ok := serversOf(t, doc)["swarmmind"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers.swarmmind 缺失")
	}
	if entry["command"] != "/fake/swarmmind-mcp" {
		t.Fatalf("落盘 command 应为探测命中的路径: %v", entry["command"])
	}
	if other := serversOf(t, doc)["other"].(map[string]any); other["command"] != "keep" {
		t.Fatalf("其他 MCP 条目应保留: %v", other)
	}
}

// TestPrimeDeclareRepeatNoDup 撤销/重钦定幂等：同 agent 反复 declare 全 200、
// 不产生重复条目、文件内始终最新凭证。
func TestPrimeDeclareRepeatNoDup(t *testing.T) {
	home := t.TempDir()
	setting := writeZcodeSetting(t, home, `{}`)
	srv, _ := newWireServer(t, home, "")
	var last *wireResp
	for i := 0; i < 3; i++ {
		resp, code := doDeclare(t, srv, "echo")
		if code != http.StatusOK || !resp.Wired {
			t.Fatalf("第 %d 次 declare 应 200 且 wired（幂等不报错）: code=%d resp=%+v", i+1, code, resp)
		}
		last = resp
	}
	doc := readSettingDoc(t, setting)
	if len(serversOf(t, doc)) != 1 {
		t.Fatalf("反复 declare 不得堆重复条目: %v", serversOf(t, doc))
	}
	if tok := entryToken(t, doc); tok != last.PrimeToken {
		t.Fatalf("文件内 token=%q 应为最新凭证 %q", tok, last.PrimeToken)
	}
}

// TestPrimeRotateRefreshesWiredToken rotate 后 token 刷新进配置：旧 token 不留
// 在宿主配置里（否则统筹载体下一请求 401）；rotate 应答带 wired 同口径字段。
func TestPrimeRotateRefreshesWiredToken(t *testing.T) {
	home := t.TempDir()
	setting := writeZcodeSetting(t, home, `{}`)
	srv, _ := newWireServer(t, home, "")
	if _, code := doDeclare(t, srv, "echo"); code != http.StatusOK {
		t.Fatalf("declare 应 200, got %d", code)
	}
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/rotate", "")
	if code != http.StatusOK {
		t.Fatalf("rotate 应 200, got %d: %s", code, out)
	}
	var resp wireResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("rotate 应答解析失败: %v\n%s", err, out)
	}
	if !resp.Wired || resp.WiredTarget != setting {
		t.Fatalf("rotate 应带 wired 同口径字段: %+v", resp)
	}
	if tok := entryToken(t, readSettingDoc(t, setting)); tok != resp.PrimeToken || tok != "tok-wire-2" {
		t.Fatalf("rotate 后配置内 token=%q 应刷新为 %q（旧 token 不得残留）", tok, resp.PrimeToken)
	}
	if strings.Contains(mustRead(t, setting), "tok-wire-1") {
		t.Fatal("旧凭证 tok-wire-1 不得残留在宿主配置")
	}
}

// TestPrimeDeclarePlaceholderCommandDegrade R4（Hana 裁决）：command 探测未
// 命中（占位符）时即使影子 home 命中 zcode 布局也一律降级——不写占位配置
//（落盘一个启动不了的 MCP 条目没有任何用处），应答保留 mcp_config 与手动指引。
func TestPrimeDeclarePlaceholderCommandDegrade(t *testing.T) {
	home := t.TempDir()
	const orig = `{"mcpServers":{"other":{"command":"keep"}}}`
	setting := writeZcodeSetting(t, home, orig)
	srv, wantPath := newWireServerCmd(t, home, "", "") // 不注桩：真实探测 → 占位符
	if wantPath != setting {
		t.Fatalf("测试布局路径不符: got %q want %q", wantPath, setting)
	}
	resp, code := doDeclare(t, srv, "echo")
	if code != http.StatusOK {
		t.Fatalf("占位符降级不影响钦定成功, got %d", code)
	}
	if resp.Wired || resp.WiredTarget != "" {
		t.Fatalf("占位符应降级 wired=false: %+v", resp)
	}
	if b, err := os.ReadFile(setting); err != nil || string(b) != orig {
		t.Fatalf("占位符降级不得写盘:\n got %s\nwant %s", b, orig)
	}
	if _, err := os.Stat(setting + primeBackupSuffix); !os.IsNotExist(err) {
		t.Fatal("占位符降级不得生成备份")
	}
	if resp.MCPConfig == nil || resp.MCPConfig["command"] != mcpCommandPlaceholder {
		t.Fatalf("降级应保留含占位 command 的 mcp_config 片段（手贴时自行补路径）: %+v", resp)
	}
	if !strings.Contains(resp.Note, "请立即配给统筹的 MCP 连接") {
		t.Fatalf("降级应保留手动指引 note: %s", resp.Note)
	}
}

// TestPrimeRotateDegradeManualFlow R3+R5：rotate 降级分支——布局未命中时
// 应答仍带 mcp_config 片段（与 declare 两口径一致，主人拿得到手贴内容），
// env 三键与新凭证同值，note 走手动指引口径。
func TestPrimeRotateDegradeManualFlow(t *testing.T) {
	home := t.TempDir() // 存在但无 .zcode 布局
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, _ := newWireServer(t, home, "")
	if resp, code := doDeclare(t, srv, "echo"); code != http.StatusOK || resp.Wired {
		t.Fatalf("declare 前置应降级 wired=false: code=%d resp=%+v", code, resp)
	}
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/rotate", "")
	if code != http.StatusOK {
		t.Fatalf("rotate 应 200, got %d: %s", code, out)
	}
	var resp wireResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("rotate 应答解析失败: %v\n%s", err, out)
	}
	if resp.Wired || resp.WiredTarget != "" {
		t.Fatalf("布局未命中 rotate 应降级 wired=false: %+v", resp)
	}
	if resp.MCPConfig == nil {
		t.Fatalf("rotate 降级应答必须带 mcp_config 片段（R3）: %s", out)
	}
	env, ok := resp.MCPConfig["env"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_config.env 缺失: %+v", resp.MCPConfig)
	}
	if tok, _ := env["SWARM_NODE_PRIME_TOKEN"].(string); tok != resp.PrimeToken || resp.PrimeToken != "tok-wire-2" {
		t.Fatalf("rotate 降级片段应带新凭证 tok-wire-2: env=%v prime_token=%q", env, resp.PrimeToken)
	}
	if url, _ := env["SWARM_NODE_URL"].(string); url != "http://127.0.0.1:8445" {
		t.Fatalf("SWARM_NODE_URL=%q", url)
	}
	if !strings.Contains(resp.Note, "请立即配给统筹的 MCP 连接") || !strings.Contains(resp.Note, "旧凭证已即时失效") {
		t.Fatalf("rotate 降级 note 应走手动指引+旧凭证失效口径: %s", resp.Note)
	}
}

// mustRead 读文件（失败 Fatal）。
func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPrimeDeclareDegradeManualFlow 降级口径：影子 home 无 zcode 布局 →
// wired=false、保留 mcp_config 片段与手动指引 note、绝不创建宿主目录。
func TestPrimeDeclareDegradeManualFlow(t *testing.T) {
	home := filepath.Join(t.TempDir(), "empty-home") // 存在但无 .zcode
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, _ := newWireServer(t, home, "")
	resp, code := doDeclare(t, srv, "echo")
	if code != http.StatusOK {
		t.Fatalf("降级不影响钦定成功, got %d", code)
	}
	if resp.Wired || resp.WiredTarget != "" {
		t.Fatalf("未命中布局应 wired=false: %+v", resp)
	}
	if resp.MCPConfig == nil || !strings.Contains(resp.Note, "请立即配给统筹的 MCP 连接") {
		t.Fatalf("降级应保留 mcp_config 片段与手动指引 note: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Fatal("降级绝不创建宿主目录（.zcode 不应存在）")
	}
	// 目录整个不存在：同口径降级，目录不被创建
	missing := filepath.Join(t.TempDir(), "no-home")
	srv2, _ := newWireServer(t, missing, "")
	if resp, code := doDeclare(t, srv2, "echo"); code != http.StatusOK || resp.Wired {
		t.Fatalf("目录不存在应降级 wired=false: code=%d resp=%+v", code, resp)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("降级绝不创建宿主目录")
	}
}

// TestPrimeDeclareConfigHomeFallback ConfigHome 为空时兜底 data_dir/home：
// 路径与 runner HOME 同口径，接线落在 data_dir/home/.zcode/cli/setting.json。
func TestPrimeDeclareConfigHomeFallback(t *testing.T) {
	dataDir := t.TempDir()
	setting := writeZcodeSetting(t, filepath.Join(dataDir, "home"), `{"theme":"dark"}`)
	srv, wantPath := newWireServer(t, "", dataDir) // echo 无 ConfigHome → 兜底
	if wantPath != setting {
		t.Fatalf("兜底路径应 data_dir/home/.zcode/cli/setting.json: got %q want %q", wantPath, setting)
	}
	resp, code := doDeclare(t, srv, "echo")
	if code != http.StatusOK || !resp.Wired || resp.WiredTarget != setting {
		t.Fatalf("兜底 home 应接线成功: code=%d resp=%+v", code, resp)
	}
	if tok := entryToken(t, readSettingDoc(t, setting)); tok != resp.PrimeToken {
		t.Fatalf("兜底 home 内 token=%q 应为 %q", tok, resp.PrimeToken)
	}
}

// TestAgentShadowHome 影子 home 解析口径直测：ConfigHome 优先，空则
// data_dir/home；agent 不在表或双空 → 空串（接线降级）。
func TestAgentShadowHome(t *testing.T) {
	dir := t.TempDir()
	echo := echoProfileLocal(t)
	echo.ConfigHome = filepath.Join(dir, "custom-home")
	s := New(Config{
		Agents:  newState(t, dir, []protocol.ExecProfile{echo}),
		DataDir: filepath.Join(dir, "data"),
	})
	if got := s.agentShadowHome("echo"); got != echo.ConfigHome {
		t.Fatalf("ConfigHome 优先: got %q want %q", got, echo.ConfigHome)
	}
	echo2 := echoProfileLocal(t) // ConfigHome 空 → 兜底 data_dir/home
	s2 := New(Config{
		Agents:  newState(t, dir, []protocol.ExecProfile{echo2}),
		DataDir: filepath.Join(dir, "data"),
	})
	if got, want := s2.agentShadowHome("echo"), filepath.Join(dir, "data", "home"); got != want {
		t.Fatalf("空 ConfigHome 应兜底 data_dir/home: got %q want %q", got, want)
	}
	if got := s2.agentShadowHome("ghost"); got != "" {
		t.Fatalf("不在表 agent 应空串, got %q", got)
	}
	s3 := New(Config{Agents: newState(t, dir, []protocol.ExecProfile{echo2})}) // 无 DataDir
	if got := s3.agentShadowHome("echo"); got != "" {
		t.Fatalf("ConfigHome 与 DataDir 双空应空串（降级）, got %q", got)
	}
}
