// enroll.go 部署包生成测试：请求校验矩阵 / node.yaml 渲染 / zip 结构（native 与
// docker）/ 内置预编译产物命中与未命中兜底 / 编译缓存命中 / 错误路径（编译失败、
// 超时、go 工具链缺失）。
//
// 真实编译用例用最小 fixture module（无第三方依赖，linux-amd64 交叉可编），
// TestMain 里建包级共享 GOCACHE：首个真实编译付全款，其余秒回；其余 zip 结构
// 用例直接驱动 writeBundleZip（假二进制文件即可），不触发 go 工具链。现场编译
// 路径用例一律经 noPrebuilt 强制绕开内置产物（本机跑过 make embed-nodes 时
// linux-amd64 等平台会命中）；预编译命中路径另有专测（无内置产物环境自动跳过）。
package api

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/transport"
)

// testGoCacheRoot 包级共享编译缓存根（TestMain 创建清理）；gocache 子目录跨用例
// 复用交叉编译中间产物，避免每次跑全量测试都重编 stdlib。
var testGoCacheRoot string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "swarmmind-enroll-test")
	if err != nil {
		panic(err)
	}
	testGoCacheRoot = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// writeFixtureModule 写最小 node 源码树（go.mod + cmd/node/main.go）；broken=true
// 时 main.go 引用未定义符号（编译必败，走 500 错误路径）。
func writeFixtureModule(t *testing.T, dir string, broken bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	main := "package main\n\nfunc main() { println(\"swarmmind fixture node\") }\n"
	if broken {
		main = "package main\n\nfunc main() { noSuchSymbolAnywhere() }\n"
	}
	files := map[string]string{
		"go.mod":           "module fixture\n\ngo 1.21\n",
		"cmd/node/main.go": main,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// newEnrollFixture 带 Enroll 配置的管理面测试基座（复用 server_test.go 的 fixture
// 结构；srcDir/cacheDir 由用例给定，cacheDir 传 "" 走独立临时目录）。基座强制
// noPrebuilt 聚焦现场编译兜底路径——否则跑过 make embed-nodes 的机器上 linux-amd64
// 等平台会被内置产物截胡；预编译命中路径见 newEnrollFixturePrebuilt。
func newEnrollFixture(t *testing.T, srcDir, cacheDir string) *fixture {
	return newEnrollFixtureS(t, srcDir, cacheDir, true)
}

// newEnrollFixturePrebuilt 同 newEnrollFixture，但保留内置预编译产物查找（默认
// 生产行为），供命中路径专测使用。
func newEnrollFixturePrebuilt(t *testing.T, srcDir, cacheDir string) *fixture {
	return newEnrollFixtureS(t, srcDir, cacheDir, false)
}

func newEnrollFixtureS(t *testing.T, srcDir, cacheDir string, forceCompile bool) *fixture {
	t.Helper()
	if cacheDir == "" {
		cacheDir = t.TempDir()
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	bus.Endpoint("hub").Subscribe(c.HandleFrame)
	s := New(Config{
		Core: c, AdminToken: "test-admin-token",
		Enroll: &EnrollConfig{SourceDir: srcDir, CacheDir: cacheDir},
	})
	if forceCompile {
		s.enroll.noPrebuilt = true
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &fixture{t: t, bus: bus, st: st, core: c, srv: srv}
}

// doFull 执行请求并保留响应头（zip 附件的 Content-Disposition 断言需要）。
func (f *fixture) doFull(hc *http.Client, method, path, body string) (*http.Response, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatalf("构造请求失败: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		f.t.Fatalf("读响应体: %v", err)
	}
	return resp, buf.Bytes()
}

const enrollGoodReq = `{"node_id":"build-linux","register_token":"` +
	`0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef` +
	`","hub_url":"wss://hub.example.com/ws/node","os":"linux","arch":"amd64","mode":"native"}`

// readZip 解包响应 zip 为 name → 条目。
func readZip(t *testing.T, data []byte) map[string]*zip.File {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("响应不是合法 zip: %v", err)
	}
	files := map[string]*zip.File{}
	for _, f := range r.File {
		files[f.Name] = f
	}
	return files
}

// zipEntryText 读条目文本内容。
func zipEntryText(t *testing.T, f *zip.File) string {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(rc); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// ---------- 校验矩阵与鉴权门 ----------

func TestEnrollValidationMatrix(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src") // 故意不存在：非法请求应在编译前被 400 拦下
	writeFixtureModule(t, src, false)
	f := newEnrollFixture(t, src, testGoCacheRoot)
	hc := f.loginClient(true)

	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct{ name, body string }{
		{"node_id 为空", `{"node_id":"","register_token":"` + tok + `","hub_url":"ws://h/ws","os":"linux","arch":"amd64"}`},
		{"node_id 过短", `{"node_id":"a","register_token":"` + tok + `","hub_url":"ws://h/ws","os":"linux","arch":"amd64"}`},
		{"node_id 大写", `{"node_id":"Build","register_token":"` + tok + `","hub_url":"ws://h/ws","os":"linux","arch":"amd64"}`},
		{"node_id 连字符开头", `{"node_id":"-abc","register_token":"` + tok + `","hub_url":"ws://h/ws","os":"linux","arch":"amd64"}`},
		{"node_id 非法字符", `{"node_id":"ab_c","register_token":"` + tok + `","hub_url":"ws://h/ws","os":"linux","arch":"amd64"}`},
		{"node_id 超长", fmt.Sprintf(`{"node_id":"%033s","register_token":"%s","hub_url":"ws://h/ws","os":"linux","arch":"amd64"}`, "a", tok)},
		{"token 非 hex", `{"node_id":"abc","register_token":"zz","hub_url":"ws://h/ws","os":"linux","arch":"amd64"}`},
		{"token 长度 63", `{"node_id":"abc","register_token":"` + tok[:63] + `","hub_url":"ws://h/ws","os":"linux","arch":"amd64"}`},
		{"token 大写 hex", `{"node_id":"abc","register_token":"` + strings.ToUpper(tok) + `","hub_url":"ws://h/ws","os":"linux","arch":"amd64"}`},
		{"hub_url 空", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"","os":"linux","arch":"amd64"}`},
		{"hub_url 非 ws 协议", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"http://hub:8443/ws/node","os":"linux","arch":"amd64"}`},
		{"hub_url 无 host", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"ws:///ws/node","os":"linux","arch":"amd64"}`},
		{"hub_url 含空格", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"ws://hu b/ws","os":"linux","arch":"amd64"}`},
		{"hub_url 含换行（yaml 注入）", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"ws://h/ws\nregister_token: evil","os":"linux","arch":"amd64"}`},
		{"os 非法", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"ws://h/ws","os":"solaris","arch":"amd64"}`},
		{"os=windows 已撤下", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"ws://h/ws","os":"windows","arch":"amd64"}`},
		{"os 缺失", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"ws://h/ws","arch":"amd64"}`},
		{"arch 非法", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"ws://h/ws","os":"linux","arch":"x86"}`},
		{"mode 非法", `{"node_id":"abc","register_token":"` + tok + `","hub_url":"ws://h/ws","os":"linux","arch":"amd64","mode":"kube"}`},
		{"请求体非 JSON", `not-json`},
	}
	for _, tc := range cases {
		code, body := f.do(hc, http.MethodPost, "/api/v1/nodes/enroll-bundle", tc.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s 应 400, got %d: %s", tc.name, code, body)
		}
	}
	// windows 撤下的人话提示（前端直接展示该 message）
	if wcode, wbody := f.do(hc, http.MethodPost, "/api/v1/nodes/enroll-bundle",
		`{"node_id":"abc","register_token":"`+tok+`","hub_url":"ws://h/ws","os":"windows","arch":"amd64"}`); wcode != http.StatusBadRequest || !strings.Contains(string(wbody), "Windows 支持开发中，暂不开放") {
		t.Fatalf("os=windows 应 400 且提示开发中, got %d: %s", wcode, wbody)
	}
	// mode 空缺省 = native（合法），docker 非法 os 无效——docker 固定 linux，此处仅证缺省可过校验前门：
	// 该请求 sourceDir 不存在，若走到编译会 500；用 mode 空省 + 假 sourceDir 会被 400 拦下与 500 区分不开，
	// 故缺省语义在 TestEnrollCompileAndCacheHit（mode 明传）覆盖。

	// 未登录 401（auth 中间件）
	code, _ := f.do(f.client(false), http.MethodPost, "/api/v1/nodes/enroll-bundle", enrollGoodReq)
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, got %d", code)
	}

	// Enroll 配置为空 → 503 降级（旧构造方不感知该端点）
	f2 := newFixture(t) // server_test.go 基座：无 Enroll
	code, body := f2.do(f2.loginClient(true), http.MethodPost, "/api/v1/nodes/enroll-bundle", enrollGoodReq)
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "enroll_disabled") {
		t.Fatalf("无 Enroll 配置应 503 enroll_disabled, got %d: %s", code, body)
	}
}

// ---------- node.yaml 渲染 ----------

func TestRenderNodeYaml(t *testing.T) {
	v := enrollVals{NodeID: "build-linux", Token: strings.Repeat("ab", 32), HubURL: "wss://hub.example.com/ws/node"}
	y := renderNodeYaml(v)
	for _, want := range []string{
		"node_id: build-linux",
		"register_token: " + v.Token,
		"hub_url: wss://hub.example.com/ws/node",
		"local_listen: 127.0.0.1:8445",
		"data_dir: data",
		"concurrency: 2",
		"# agents:", // agents 段注释示例
	} {
		if !strings.Contains(y, want) {
			t.Fatalf("node.yaml 缺 %q\n%s", want, y)
		}
	}
	// 真实值不得进注释行（保证 node 进程按明文字段解析，而非被注释吞掉）
	for _, line := range strings.Split(y, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") &&
			(strings.Contains(line, v.Token) || strings.Contains(line, v.NodeID+": ")) {
			t.Fatalf("真实配置值出现在注释行: %s", line)
		}
	}
}

// ---------- zip 结构（assembly 层，假二进制） ----------

func TestNativeZipStructure(t *testing.T) {
	dummy := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(dummy, []byte("FAKEBINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	v := enrollVals{NodeID: "build-linux", Token: strings.Repeat("cd", 32), HubURL: "ws://127.0.0.1:8443/ws/node"}

	cases := []struct {
		goos      string
		exe       string
		script    string
		scriptHas []string
	}{
		{"linux", "swarmmind-node", "install.sh", []string{
			"systemctl enable --now swarmmind-node", "/etc/systemd/system/swarmmind-node.service",
			"chmod +x swarmmind-node", "chmod 0600 /opt/swarmmind/node.yaml"}},
		{"darwin", "swarmmind-node", "install.sh", []string{
			// plist 由脚本现场生成（此前只 load 不生成，安装链路断裂）：路径、
			// 程序参数（绝对路径二进制 + --config + node.yaml）、工作目录、
			// 常驻与开机自启、launchctl load 全链断言
			`cat > "$PLIST"`, "com.swarmmind.node.plist", "~/Library/LaunchAgents",
			"<key>ProgramArguments</key>", "<string>$DIR/swarmmind-node</string>",
			"<string>--config</string>", "<string>$DIR/node.yaml</string>",
			"<key>WorkingDirectory</key><string>$DIR</string>",
			"<key>KeepAlive</key><true/>", "<key>RunAtLoad</key><true/>",
			"launchctl load", "chmod +x swarmmind-node", `chmod 0600 "$DIR/node.yaml"`}},
		{"windows", "swarmmind-node.exe", "install.bat", []string{"sc create swarmmind-node binPath=", "sc start swarmmind-node"}},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		if err := writeBundleZip(zw, "native", tc.goos, "amd64", dummy, nil, v); err != nil {
			t.Fatalf("%s: %v", tc.goos, err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		files := readZip(t, buf.Bytes())
		want := map[string]bool{tc.exe: true, "node.yaml": true, tc.script: true, "README.txt": true}
		if len(files) != len(want) {
			t.Fatalf("%s: 条目应为 %v, got %v", tc.goos, want, names(files))
		}
		for name := range want {
			if files[name] == nil {
				t.Fatalf("%s: 缺条目 %s（有 %v）", tc.goos, name, names(files))
			}
		}
		// 二进制与安装脚本须带可执行位
		for _, name := range []string{tc.exe, tc.script} {
			if files[name].Mode()&0o111 == 0 {
				t.Fatalf("%s: %s 应有可执行位, mode=%v", tc.goos, name, files[name].Mode())
			}
		}
		script := zipEntryText(t, files[tc.script])
		for _, marker := range tc.scriptHas {
			if !strings.Contains(script, marker) {
				t.Fatalf("%s: %s 缺 %q\n%s", tc.goos, tc.script, marker, script)
			}
		}
		// node.yaml 内嵌真实值、权限 0600（内含 register_token，落地即属主可读写）；
		// README 给出三步指引
		yamlTxt := zipEntryText(t, files["node.yaml"])
		if !strings.Contains(yamlTxt, "node_id: build-linux") || !strings.Contains(yamlTxt, v.Token) {
			t.Fatalf("%s: node.yaml 未内嵌真实值\n%s", tc.goos, yamlTxt)
		}
		if got := files["node.yaml"].Mode().Perm(); got != 0o600 {
			t.Fatalf("%s: node.yaml 权限应 0600, got %v", tc.goos, got)
		}
		// README 提示令牌敏感、勿转发（zip 外部提示，0600 纪律的一部分）
		if readme := zipEntryText(t, files["README.txt"]); !strings.Contains(readme, "0600") {
			t.Fatalf("%s: README 缺令牌权限提示", tc.goos)
		}
		if !strings.Contains(zipEntryText(t, files["README.txt"]), "3. 看状态") {
			t.Fatalf("%s: README 缺三步指引", tc.goos)
		}
	}
}

func TestDockerZipStructure(t *testing.T) {
	dummy := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(dummy, []byte("FAKEBINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	v := enrollVals{NodeID: "build-linux", Token: strings.Repeat("ef", 32), HubURL: "wss://hub.example.com/ws/node"}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := writeBundleZip(zw, "docker", "linux", "arm64", dummy, nil, v); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	files := readZip(t, buf.Bytes())
	for _, name := range []string{"Dockerfile", "docker-compose.yml", "swarmmind-node", "node.yaml", "README.txt"} {
		if files[name] == nil {
			t.Fatalf("docker 包缺条目 %s（有 %v）", name, names(files))
		}
	}
	if len(files) != 5 {
		t.Fatalf("docker 包条目应 5 个, got %v", names(files))
	}
	if got := zipEntryText(t, files["Dockerfile"]); !strings.Contains(got, "FROM scratch") ||
		!strings.Contains(got, `ENTRYPOINT ["/swarmmind-node", "--config", "/node.yaml"]`) {
		t.Fatalf("Dockerfile 内容不对:\n%s", got)
	}
	comp := zipEntryText(t, files["docker-compose.yml"])
	for _, marker := range []string{"restart: unless-stopped", "./node.yaml:/node.yaml", "./data:/data"} {
		if !strings.Contains(comp, marker) {
			t.Fatalf("docker-compose.yml 缺 %q:\n%s", marker, comp)
		}
	}
	// docker 固定 linux 二进制：exe 名不带 .exe 后缀且可执行位在
	if files["swarmmind-node"].Mode()&0o111 == 0 {
		t.Fatal("docker 包内二进制应有可执行位")
	}
}

// ---------- 内置预编译产物：命中直出 / 未命中兜底 ----------

// TestPrebuiltNodeHit 命中路径：本机跑过 make embed-nodes 有真实产物时可测；
// 无产物环境（fresh clone）跳过——未命中兜底语义由既有编译用例覆盖。
func TestPrebuiltNodeHit(t *testing.T) {
	if _, err := fs.Stat(embedFS, "embedded/node-linux-amd64.gz"); err != nil {
		t.Skip("本机无内置预编译产物（未跑 make embed-nodes），跳过命中用例")
	}
	data, ok := prebuiltNode("linux", "amd64")
	if !ok || len(data) == 0 {
		t.Fatalf("内置产物应命中且非空, ok=%v len=%d", ok, len(data))
	}
	if !bytes.HasPrefix(data, []byte("\x7fELF")) {
		t.Fatalf("linux-amd64 内置产物应为 ELF, got %q", data[:min(16, len(data))])
	}
}

func TestPrebuiltNodeMiss(t *testing.T) {
	if data, ok := prebuiltNode("plan9", "mips"); ok {
		t.Fatalf("不存在的平台不应命中, len=%d", len(data))
	}
}

// TestPrebuiltMagicCheck magic 头校验矩阵：各平台合法头放行（darwin 认 Mach-O
// 64 与 fat 二进制两种）、空内容/垃圾内容/平台错配/未知平台一律拒绝——坏产物
// 视为未命中，走现场编译兜底，绝不进部署包。
func TestPrebuiltMagicCheck(t *testing.T) {
	elf := append([]byte("\x7fELF"), make([]byte, 64)...)
	macho := append([]byte("\xcf\xfa\xfe\xed"), make([]byte, 64)...)
	fat := append([]byte("\xca\xfe\xba\xbe"), make([]byte, 64)...)
	pe := append([]byte("MZ"), make([]byte, 64)...)
	ok := []struct {
		goos string
		data []byte
	}{
		{"linux", elf}, {"darwin", macho}, {"darwin", fat}, {"windows", pe},
	}
	for _, tc := range ok {
		if !prebuiltMagicOK(tc.goos, tc.data) {
			t.Fatalf("%s 合法 magic 应放行: % x", tc.goos, tc.data[:4])
		}
	}
	bad := []struct {
		goos string
		data []byte
	}{
		{"linux", nil},                         // 空产物
		{"linux", []byte("garbage-not-a-bin")}, // 垃圾内容（占位污染）
		{"darwin", elf},                        // 平台错配
		{"windows", elf},                       // 平台错配
		{"plan9", elf},                         // 白名单外平台恒拒
		{"linux", []byte("\x7fEL")},            // magic 头不完整
	}
	for _, tc := range bad {
		if prebuiltMagicOK(tc.goos, tc.data) {
			t.Fatalf("%s 非法产物(%q)应拒绝", tc.goos, tc.data)
		}
	}
}

// TestEnrollPrebuiltBundle 端到端命中：sourceDir 故意不存在——若错误地走了现场
// 编译会 500，200 即证明二进制来自 embed；并断言未写编译缓存（跳过缓存语义）。
func TestEnrollPrebuiltBundle(t *testing.T) {
	if _, err := fs.Stat(embedFS, "embedded/node-linux-amd64.gz"); err != nil {
		t.Skip("本机无内置预编译产物（未跑 make embed-nodes），跳过端到端命中用例")
	}
	cacheDir := t.TempDir()
	f := newEnrollFixturePrebuilt(t, filepath.Join(t.TempDir(), "no-such-src"), cacheDir)
	hc := f.loginClient(true)
	resp, data := f.doFull(hc, http.MethodPost, "/api/v1/nodes/enroll-bundle", enrollGoodReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("命中内置产物应 200, got %d: %s", resp.StatusCode, data)
	}
	files := readZip(t, data)
	bin := files["swarmmind-node"]
	if bin == nil {
		t.Fatalf("zip 缺二进制（有 %v）", names(files))
	}
	if got := zipEntryText(t, bin); !strings.HasPrefix(got, "\x7fELF") {
		t.Fatalf("内置产物应为 ELF, got %q", got[:min(16, len(got))])
	}
	if bins, _ := filepath.Glob(filepath.Join(cacheDir, "bins", "*")); len(bins) != 0 {
		t.Fatalf("命中内置产物不应写编译缓存: %v", bins)
	}
}

// ---------- 真实编译：成功 + 缓存命中（linux-amd64 交叉可编，包级共享 GOCACHE） ----------

func TestEnrollCompileAndCacheHit(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	writeFixtureModule(t, src, false)
	f := newEnrollFixture(t, src, testGoCacheRoot)
	hc := f.loginClient(true)

	// 第一次：现场编译 → 200 zip
	resp, data := f.doFull(hc, http.MethodPost, "/api/v1/nodes/enroll-bundle", enrollGoodReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首编应 200, got %d: %s", resp.StatusCode, data)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type 应 application/zip, got %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="swarmmind-node-linux-amd64.zip"`) {
		t.Fatalf("附件名不对: %q", cd)
	}
	files := readZip(t, data)
	bin := files["swarmmind-node"]
	if bin == nil {
		t.Fatalf("zip 缺二进制（有 %v）", names(files))
	}
	if got := zipEntryText(t, bin); !strings.HasPrefix(got, "\x7fELF") {
		t.Fatalf("linux-amd64 产物应为 ELF, got %q", got[:min(16, len(got))])
	}
	yamlTxt := zipEntryText(t, files["node.yaml"])
	if !strings.Contains(yamlTxt, "register_token: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") ||
		!strings.Contains(yamlTxt, "node_id: build-linux") ||
		!strings.Contains(yamlTxt, "hub_url: wss://hub.example.com/ws/node") {
		t.Fatalf("zip 内 node.yaml 未内嵌真实值:\n%s", yamlTxt)
	}

	// 缓存命中证明：把缓存里的二进制替换成哨兵内容，第二次请求直接复用（重编译会覆盖回真二进制）
	bins, err := filepath.Glob(filepath.Join(testGoCacheRoot, "bins", "*", "swarmmind-node"))
	if err != nil || len(bins) != 1 {
		t.Fatalf("应恰好缓存一份 linux-amd64 产物: %v %v", bins, err)
	}
	sentinel := []byte("SENTINEL-CACHE-HIT")
	if err := os.WriteFile(bins[0], sentinel, 0o755); err != nil {
		t.Fatal(err)
	}
	resp2, data2 := f.doFull(hc, http.MethodPost, "/api/v1/nodes/enroll-bundle", enrollGoodReq)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("二编应 200, got %d", resp2.StatusCode)
	}
	bin2 := readZip(t, data2)["swarmmind-node"]
	if got := zipEntryText(t, bin2); got != string(sentinel) {
		t.Fatalf("第二次请求应命中缓存（返回哨兵内容）, got %q", got)
	}
}

// ---------- 错误路径 ----------

func TestEnrollCompileFailure(t *testing.T) {
	src := t.TempDir()
	writeFixtureModule(t, src, true) // 引用未定义符号 → 编译必败
	cacheDir := t.TempDir()
	f := newEnrollFixture(t, src, cacheDir)
	code, body := f.do(f.loginClient(true), http.MethodPost, "/api/v1/nodes/enroll-bundle", enrollGoodReq)
	if code != http.StatusInternalServerError {
		t.Fatalf("编译失败应 500, got %d: %s", code, body)
	}
	if !strings.Contains(string(body), "compile_failed") {
		t.Fatalf("应带 compile_failed 错误码: %s", body)
	}
	// 半成品不得留在缓存：bins 下不应有任何产物文件
	bins, _ := filepath.Glob(filepath.Join(cacheDir, "bins", "*", "*"))
	for _, p := range bins {
		t.Fatalf("编译失败不应留下缓存产物: %s", p)
	}
}

func TestEnrollCompileFailureUnit(t *testing.T) {
	src := t.TempDir()
	writeFixtureModule(t, src, true)
	b := newEnrollBuilder(&EnrollConfig{SourceDir: src, CacheDir: t.TempDir()})
	b.noPrebuilt = true // linux-amd64 在内置产物集合内，强制走现场编译兜底路径
	_, err := b.binary("linux", "amd64")
	var ee *enrollError
	if err == nil {
		t.Fatal("坏源码应报错")
	}
	if !errors.As(err, &ee) || ee.status != http.StatusInternalServerError || ee.code != "compile_failed" {
		t.Fatalf("应 500 compile_failed, got %v", err)
	}
	if !strings.Contains(ee.msg, "noSuchSymbolAnywhere") {
		t.Fatalf("错误消息应带编译器输出，便于现场排障: %s", ee.msg)
	}
}

func TestEnrollCompileTimeout(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	writeFixtureModule(t, src, false)
	b := newEnrollBuilder(&EnrollConfig{SourceDir: src, CacheDir: t.TempDir()})
	b.noPrebuilt = true // linux-arm64 在内置产物集合内，强制走现场编译兜底路径
	b.timeout = 1       // 纳秒级超时：必走超时路径（linux-arm64 未被他用例编译，必未命中缓存）
	_, err := b.binary("linux", "arm64")
	var ee *enrollError
	if !errors.As(err, &ee) || ee.status != http.StatusServiceUnavailable || ee.code != "compile_timeout" {
		t.Fatalf("应 503 compile_timeout, got %v", err)
	}
	if !strings.Contains(ee.msg, "首次编译约一分钟，请稍后重试") {
		t.Fatalf("超时提示语不对: %s", ee.msg)
	}
}

func TestEnrollGoBinMissing(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	writeFixtureModule(t, src, false)
	b := newEnrollBuilder(&EnrollConfig{SourceDir: src, CacheDir: t.TempDir()})
	b.goBin = "/nonexistent/go-toolchain-xyz" // 注入假工具链路径 → exec 失败
	b.noPrebuilt = true                       // linux-amd64 在内置产物集合内，强制走现场编译兜底路径
	_, err := b.binary("linux", "amd64")
	var ee *enrollError
	if !errors.As(err, &ee) || ee.status != http.StatusInternalServerError || ee.code != "compile_failed" {
		t.Fatalf("假工具链应 500 compile_failed, got %v", err)
	}
}

// ---------- 安装脚本语法（sh -n）：heredoc/plist 生成结构写错即装机失败 ----------

func TestInstallShSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("环境无 sh，跳过安装脚本语法校验")
	}
	for _, goos := range []string{"linux", "darwin"} {
		script := unixInstallSh(goos)
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s install.sh 语法错误: %v\n%s", goos, err, out)
		}
	}
}

// ---------- 工具 ----------

func names(files map[string]*zip.File) []string {
	out := make([]string, 0, len(files))
	for k := range files {
		out = append(out, k)
	}
	return out
}
