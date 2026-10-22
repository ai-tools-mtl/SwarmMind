// 节点部署包生成（2026-10-22，接入向导 Step3 数据源）。
//
// POST /api/v1/nodes/enroll-bundle（管理端鉴权）：按 {node_id, register_token,
// hub_url, os, arch, mode} 组装部署包 zip——native 模式（二进制 + node.yaml +
// install.sh/install.bat + README）或 docker 模式（Dockerfile + docker-compose.yml
// + linux 二进制 + node.yaml + README）。二进制来源优先级：内置预编译产物
// （make embed-nodes 生成五平台 gzip 到 embedded/，go:embed 进 hub 二进制，
// 命中秒出包，跳过编译/缓存/互斥）→ 未命中（开发环境未跑 embed-nodes、或
// 平台不在内置集合）现场交叉编译兜底，编译结果按 (os,arch,commit) 缓存到
// CacheDir 复用，同 key 并发只编译一次；编译超时（90s）返回 503 提示稍后
// 重试（首次编译约一分钟）。
//
// 安全约束：
//   - go build 参数全部代码拼装（exec.Command("go", "build", ...)），不经 shell，
//     无注入面；node_id/token/hub_url 先过严格白名单再渲染进 node.yaml（yaml
//     纯量不含引号/控制字符即可注入免疫），二进制与脚本内容全部服务端常量。
//   - 产物内嵌 register_token 凭据，属人工装机能力：端点只对管理端会话开放，
//     不设 MCP 工具（SPEC 六.六豁免），部署包不经 agent 面分发。
package api

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// EnrollConfig 部署包编译参数（Config.Enroll；nil 时端点 503 降级，旧构造方不感知）。
type EnrollConfig struct {
	// SourceDir node 源码根（含 cmd/node）；装配方取 SWARM_SOURCE_DIR，
	// 默认 /opt/swarmmind-src。
	SourceDir string
	// CacheDir 编译缓存根：bins/<os>-<arch>-<commit>/ 存编译产物，
	// gocache/ 为 go build 的 GOCACHE。
	CacheDir string
}

// enroll 编译器；Server 持有（Enroll 配置非空时构建）。
type enrollBuilder struct {
	sourceDir string
	cacheDir  string
	goBin     string        // go 工具链；空 = LookPath 现查（测试可注入假路径走失败路径）
	goProxy   string        // 模块代理；空 = https://goproxy.cn,direct（国内可达兜底）
	timeout   time.Duration // 单次编译超时（90s → 503）

	commitOnce sync.Once
	commitVal  string // 源码树 commit（缓存键第三元；进程内惰性解析一次）

	mu    sync.Mutex
	locks map[string]*sync.Mutex // 缓存键 → 编译互斥（同 key 并发合并为一次编译）

	noPrebuilt bool // true = 跳过内置产物查找，强制现场编译兜底路径（测试注入）
}

// prebuiltBinary 内置预编译产物查找入口（noPrebuilt 时恒未命中，测试用）。
func (b *enrollBuilder) prebuiltBinary(goos, goarch string) ([]byte, bool) {
	if b.noPrebuilt {
		return nil, false
	}
	return prebuiltNode(goos, goarch)
}

// ---------- 内置预编译产物 ----------

// embedFS make embed-nodes 生成的五平台 node 预编译产物（gzip）+ 占位文件。
// placeholder.txt 保证目录非空可编译；产物本身不入库（.gitignore 排除），
// 未生成时（开发环境 fresh clone）目录内只有占位文件，查找恒未命中走现场编译。
//
//go:embed all:embedded
var embedFS embed.FS

// prebuiltMagic 平台 → 可执行二进制 magic 头。内置 gzip 产物入库前未经执行
// 验证，解压后须核对 magic：不符（文件损坏/被占位内容污染）一律视为未命中，
// 走现场编译兜底，绝不把坏产物打进部署包。darwin 同时认 Mach-O 64 位
// （\xcf\xfa\xfe\xed）与通用二进制（\xca\xfe\xba\xbe）两种形态。
var prebuiltMagic = map[string][][]byte{
	"linux":   {[]byte("\x7fELF")},
	"darwin":  {[]byte("\xcf\xfa\xfe\xed"), []byte("\xca\xfe\xba\xbe")},
	"windows": {[]byte("MZ")},
}

// prebuiltMagicOK 校验解压产物非空且 magic 头与平台匹配（未知平台恒 false——
// 白名单外平台的内置产物不可信，宁可多编一次）。
func prebuiltMagicOK(goos string, data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, m := range prebuiltMagic[goos] {
		if bytes.HasPrefix(data, m) {
			return true
		}
	}
	return false
}

// prebuiltNode 取内置预编译 node 二进制（embedded/node-<goos>-<goarch>.gz 解压
// 原文）。对应平台无内置产物、解压失败、产物为空或 magic 头不符时 ok=false，
// 调用方走现场编译兜底。
func prebuiltNode(goos, goarch string) ([]byte, bool) {
	f, err := embedFS.Open(fmt.Sprintf("embedded/node-%s-%s.gz", goos, goarch))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, false
	}
	data, err := io.ReadAll(gz)
	if err != nil || !prebuiltMagicOK(goos, data) {
		if err == nil {
			log.Printf("[hub] 内置预编译产物 %s/%s magic 头不符（损坏或被污染），改走现场编译兜底", goos, goarch)
		}
		return nil, false
	}
	return data, true
}

// proxyOr 配置兜底：未配置时用国内可达镜像（goproxy.cn）+ 直连回退。
func (b *enrollBuilder) proxyOr(def string) string {
	if b.goProxy != "" {
		return b.goProxy
	}
	return def
}

// 编译纪律常量。
const (
	enrollCompileTimeout = 90 * time.Second
	enrollBinName        = "swarmmind-node"
	enrollMaxBodyBytes   = 4 << 10 // 请求体只有六个短字段，4KiB 足够
)

func newEnrollBuilder(cfg *EnrollConfig) *enrollBuilder {
	return &enrollBuilder{
		sourceDir: cfg.SourceDir,
		cacheDir:  cfg.CacheDir,
		timeout:   enrollCompileTimeout,
		locks:     map[string]*sync.Mutex{},
	}
}

// ---------- 请求校验 ----------

// enrollRequest POST /api/v1/nodes/enroll-bundle 请求体。
type enrollRequest struct {
	NodeID        string `json:"node_id"`
	RegisterToken string `json:"register_token"`
	HubURL        string `json:"hub_url"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Mode          string `json:"mode"` // native|docker；空 = native
}

// node_id/token/hub_url 三条白名单已下沉 protocol 包同源（方案 3.6，2026-10-22）：
// 本处只调用 protocol.ValidXxx，规则改动一处生效、两端不漂移。三者会原样渲染进
// node.yaml 与文件名，超界即拒（400）。

// normalize 校验请求并归一（mode 空回落 native；docker 模式固定打包 linux
// 二进制——容器基座即 linux，os 字段不参与 docker 产物）。非法返回人话原因。
func (r *enrollRequest) normalize() (osArg, archArg, mode string, err error) {
	if !protocol.ValidNodeID(r.NodeID) {
		return "", "", "", protocol.ErrJoinNodeID
	}
	if !protocol.ValidToken(r.RegisterToken) {
		return "", "", "", protocol.ErrJoinToken
	}
	if !protocol.ValidHubURL(r.HubURL) {
		return "", "", "", protocol.ErrJoinHubURL
	}
	// Windows 暂撤（2026-10-22 双审决断：安装链路未经真实机验证，撤下待开发
	// 完整后再开放；内置 windows 产物与打包代码保留，未来开放即用）。
	switch r.OS {
	case "linux", "darwin":
	case "windows":
		return "", "", "", fmt.Errorf("Windows 支持开发中，暂不开放")
	case "":
		return "", "", "", fmt.Errorf("os 必填（linux|darwin）")
	default:
		return "", "", "", fmt.Errorf("os 仅支持 linux|darwin")
	}
	switch r.Arch {
	case "amd64", "arm64":
	case "":
		return "", "", "", fmt.Errorf("arch 必填（amd64|arm64）")
	default:
		return "", "", "", fmt.Errorf("arch 仅支持 amd64|arm64")
	}
	switch r.Mode {
	case "", "native":
		mode = "native"
	case "docker":
		mode = "docker"
	default:
		return "", "", "", fmt.Errorf("mode 仅支持 native|docker")
	}
	if mode == "docker" {
		return "linux", r.Arch, mode, nil
	}
	return r.OS, r.Arch, mode, nil
}

// ---------- HTTP 端点 ----------

// handleEnrollInfo GET /api/v1/nodes/enroll-info：节点接入入口真实地址（接入向导
// Step3 的 hub_url 预填数据源，第六轮修复 2）。管理台（api_listen）与节点入口
// （node_listen）端口不同，前端原按控制台地址推导接入地址——生成的接入串/
// node.yaml 全部指到管理台端口，节点按串接入连不上。host 规则：node_listen 显式
// 绑定回环（insecure_loopback 模式经 session.ValidateLoopback 强制）时仅本机
// 节点可连，一律回环字面量；全接口或显式外部绑定取请求 Host 的主机名（浏览器
// 可达本机之所经，节点入口与管理台同主机可达）。路径 /ws/node 与 cmd/hub 的
// 会话路由同源（mux.Handle("GET /ws/node")）。503 = NodeEntry 未装配（旧构造方），
// 前端回落本地推导。
func (s *Server) handleEnrollInfo(w http.ResponseWriter, r *http.Request) {
	e := s.cfg.NodeEntry
	if e == nil || e.Listen == "" {
		writeErr(w, http.StatusServiceUnavailable, "enroll_info_disabled",
			"hub 未配置节点接入入口（api.Config.NodeEntry），无法提供真实接入地址")
		return
	}
	host, port, err := net.SplitHostPort(e.Listen)
	if err != nil || port == "" {
		writeErr(w, http.StatusInternalServerError, "internal", "节点接入入口地址非法: "+e.Listen)
		return
	}
	if host != "127.0.0.1" && host != "::1" {
		// 非回环绑定：host 取请求主机名（去端口与 IPv6 方括号）；请求无 Host（罕见）
		// 回落 127.0.0.1，至少本机接入可用。
		if h := enrollReqHost(r); h != "" {
			host = h
		} else {
			host = "127.0.0.1"
		}
	}
	scheme := e.Scheme
	if scheme != "ws" {
		scheme = "wss" // 未配置协议按 TLS 模式默认（insecure 部署显式传 ws）
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_ws_url": scheme + "://" + net.JoinHostPort(host, port) + "/ws/node",
	})
}

// enrollReqHost 请求 Host 的主机名部分（IPv6 形态 [::1]:p 与裸主机名都归一）。
func enrollReqHost(r *http.Request) string {
	h := strings.TrimSpace(r.Host)
	if h == "" {
		return ""
	}
	if hh, _, err := net.SplitHostPort(h); err == nil {
		return hh
	}
	return strings.Trim(h, "[]")
}

// handleEnroll POST /api/v1/nodes/enroll-bundle：校验 → 取/编译缓存二进制 →
// zip 组装 → 附件流。400 参数非法 / 401 未登录（auth 中间件）/ 503 未启用或
// 编译超时 / 500 编译失败（带 stderr 尾巴，便于装机现场排障）。
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if s.enroll == nil {
		writeErr(w, http.StatusServiceUnavailable, "enroll_disabled", "hub 未配置部署包编译参数（Enroll），无法生成部署包")
		return
	}
	var req enrollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, enrollMaxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 JSON {node_id, register_token, hub_url, os, arch, mode}")
		return
	}
	osArg, archArg, mode, err := req.normalize()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	// 二进制来源：内置预编译产物优先（秒出包，跳过编译/缓存/互斥）；未命中
	// （未跑 make embed-nodes 或平台不在内置集合）走现场编译兜底。
	var binPath string
	var binData []byte
	if data, ok := s.enroll.prebuiltBinary(osArg, archArg); ok {
		binData = data
		log.Printf("[hub] 部署包使用内置预编译产物 %s/%s（commit %s）", osArg, archArg, s.enroll.commit())
	} else {
		log.Printf("[hub] 部署包无内置预编译产物 %s/%s，走现场编译兜底", osArg, archArg)
		var err error
		binPath, err = s.enroll.binary(osArg, archArg)
		if err != nil {
			var ee *enrollError
			if errors.As(err, &ee) {
				writeErr(w, ee.status, ee.code, ee.msg)
			} else {
				log.Printf("[hub] 部署包编译异常: %v", err)
				writeErr(w, http.StatusInternalServerError, "internal", "部署包生成失败")
			}
			return
		}
	}
	filename := fmt.Sprintf("%s-%s-%s.zip", enrollBinName, osArg, archArg) // native：SPEC 六命名
	if mode == "docker" {
		filename = fmt.Sprintf("%s-docker-linux-%s.zip", enrollBinName, archArg)
	}
	vals := enrollVals{NodeID: req.NodeID, Token: req.RegisterToken, HubURL: req.HubURL}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := writeBundleZip(zw, mode, osArg, archArg, binPath, binData, vals); err != nil {
		log.Printf("[hub] 部署包 zip 组装失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "部署包生成失败")
		return
	}
	if err := zw.Close(); err != nil {
		log.Printf("[hub] 部署包 zip 收尾失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal", "部署包生成失败")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// enrollError 带 HTTP 语义的编译/打包错误。
type enrollError struct {
	status int
	code   string
	msg    string
}

func (e *enrollError) Error() string { return e.msg }

// ---------- 编译与缓存 ----------

// binary 取 (os,arch) 目标编译产物路径：缓存命中直接复用，未命中按
// (os,arch,commit) 锁内编译（同 key 并发只编一次）。产物键中的 commit 取
// 源码树当前 HEAD——拉新代码后自然换键重编，旧产物留作回滚复用。
func (b *enrollBuilder) binary(goos, goarch string) (string, error) {
	exe := enrollBinName
	if goos == "windows" {
		exe += ".exe"
	}
	key := fmt.Sprintf("%s-%s-%s", goos, goarch, b.commit())
	dst := filepath.Join(b.cacheDir, "bins", key, exe)
	if enrollFileExists(dst) {
		return dst, nil
	}
	mu := b.lock(key)
	mu.Lock()
	defer mu.Unlock()
	if enrollFileExists(dst) { // 等锁期间别人可能已编译完
		return dst, nil
	}
	if err := b.compile(goos, goarch, dst); err != nil {
		return "", err
	}
	log.Printf("[hub] 部署包二进制已编译并缓存 %s", dst)
	return dst, nil
}

// compile 交叉编译 ./cmd/node 到 dst。命令与参数全部代码拼装（不经 shell），
// 目标平台经 GOOS/GOARCH 环境变量注入，CGO 关死保证交叉可编、产物静态可搬；
// GOCACHE 指 CacheDir/gocache（跨请求复用编译中间产物，二次打包秒回）。
// GOTOOLCHAIN=local：hub 机器 go 版本不满足源码要求时报清晰错误，而非现场
// 下载工具链。超时（90s）→ 503 compile_timeout「首次编译约一分钟，请稍后重试」。
func (b *enrollBuilder) compile(goos, goarch, dst string) error {
	goBin := b.goBin
	if goBin == "" {
		p, err := exec.LookPath("go")
		if err != nil {
			// systemd 服务默认 PATH 不含 /usr/local/go/bin——常见安装位逐个探测
			for _, cand := range []string{"/usr/local/go/bin/go", "/usr/lib/go/bin/go", "/opt/go/bin/go", "/usr/local/bin/go", "/snap/bin/go"} {
				if st, statErr := os.Stat(cand); statErr == nil && !st.IsDir() {
					p = cand
					err = nil
					break
				}
			}
		}
		if err != nil {
			return &enrollError{http.StatusServiceUnavailable, "compile_unavailable",
				"hub 服务器未安装 go 工具链，无法编译部署包（安装 go 或设置 SWARM_SOURCE_DIR 后重启，" +
					"或运行 make embed-nodes 后重新构建 hub 以内置预编译产物）"}
		}
		goBin = p
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("创建编译缓存目录失败: %w", err)
	}
	timeout := b.timeout
	if timeout <= 0 {
		timeout = enrollCompileTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	log.Printf("[hub] 部署包编译开始 %s/%s → %s（超时 %s）", goos, goarch, dst, timeout)
	cmd := exec.CommandContext(ctx, goBin, "build", "-o", dst, "./cmd/node")
	cmd.Dir = b.sourceDir
	cmd.Env = append(os.Environ(),
		"GOOS="+goos,
		"GOARCH="+goarch,
		"CGO_ENABLED=0",
		"GOCACHE="+filepath.Join(b.cacheDir, "gocache"),
		// 模块缓存持久化 + 国内镜像兜底：hub 服务用户首编译冷启动时，
		// 默认 proxy.golang.org 在国内服务器不可达（2026-10-22 实测超时）
		"GOPATH="+filepath.Join(b.cacheDir, "gopath"),
		"GOMODCACHE="+filepath.Join(b.cacheDir, "gopath", "pkg", "mod"),
		"GOPROXY="+b.proxyOr("https://goproxy.cn,direct"),
		"GOTOOLCHAIN=local",
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		_ = os.Remove(dst) // 清掉半成品
		return &enrollError{http.StatusServiceUnavailable, "compile_timeout",
			"首次编译约一分钟，请稍后重试"}
	}
	if err != nil {
		_ = os.Remove(dst)
		log.Printf("[hub] 部署包编译失败 %s/%s: %v\n%s", goos, goarch, err, enrollTail(out))
		return &enrollError{http.StatusInternalServerError, "compile_failed",
			"部署包编译失败（源码目录 " + b.sourceDir + "）：" + enrollTail(out)}
	}
	return nil
}

// commit 源码树 commit（缓存键第三元）：优先 git rev-parse（反映将编译的源码），
// 回落 hub 自身构建信息里的 vcs.revision，再回落 "unknown"（仍可缓存，只是失去
// 换版失效语义）。进程内解析一次；输出收紧为小写 hex，防怪值进路径。
func (b *enrollBuilder) commit() string {
	b.commitOnce.Do(func() { b.commitVal = b.resolveCommit() })
	return b.commitVal
}

func (b *enrollBuilder) resolveCommit() string {
	if out, err := exec.Command("git", "-C", b.sourceDir, "rev-parse", "HEAD").Output(); err == nil {
		if c := enrollSanitizeCommit(string(out)); c != "" {
			return c
		}
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				if c := enrollSanitizeCommit(s.Value); c != "" {
					return c
				}
			}
		}
	}
	return "unknown"
}

func enrollSanitizeCommit(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) == 0 || len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return ""
		}
	}
	return s
}

func (b *enrollBuilder) lock(key string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	mu, ok := b.locks[key]
	if !ok {
		mu = &sync.Mutex{}
		b.locks[key] = mu
	}
	return mu
}

func enrollFileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// enrollTail 编译错误输出尾巴（去空白，截 512 字节）——500 响应与日志共用。
func enrollTail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 512 {
		s = s[:512] + "…"
	}
	if s == "" {
		return "无编译输出"
	}
	return s
}

// ---------- zip 组装 ----------

// enrollVals 渲染 node.yaml 的三元组（全部过完白名单才进这里）。
type enrollVals struct {
	NodeID string
	Token  string
	HubURL string
}

// writeBundleZip 打包部署包。native：二进制 + node.yaml + 安装脚本 + README；
// docker：Dockerfile + docker-compose.yml + linux 二进制 + node.yaml + README。
// 二进制来源二选一：binData 非空（内置预编译产物，内存直入包）或 binPath
// （现场编译产物，io.Copy 从磁盘流式入包，不整块进内存）。
func writeBundleZip(zw *zip.Writer, mode, goos, goarch, binPath string, binData []byte, v enrollVals) error {
	exe := enrollBinName
	if mode != "docker" && goos == "windows" {
		exe += ".exe"
	}
	var err error
	if binData != nil {
		err = zipEntryFromBytes(zw, exe, 0o755, binData)
	} else {
		err = zipEntryFromFile(zw, exe, 0o755, binPath)
	}
	if err != nil {
		return err
	}
	// node.yaml 内嵌 register_token 凭据，zip 外部权限一并 0600（解压落地即
	// 属主可读写；安装脚本内另有 chmod 兜底），README 提示勿转发明文。
	if err := zipEntry(zw, "node.yaml", 0o600, renderNodeYaml(v)); err != nil {
		return err
	}
	switch {
	case mode == "docker":
		if err := zipEntry(zw, "Dockerfile", 0o644, dockerfile()); err != nil {
			return err
		}
		if err := zipEntry(zw, "docker-compose.yml", 0o644, dockerCompose()); err != nil {
			return err
		}
	case goos == "windows":
		if err := zipEntry(zw, "install.bat", 0o755, windowsInstallBat()); err != nil {
			return err
		}
	default:
		if err := zipEntry(zw, "install.sh", 0o755, unixInstallSh(goos)); err != nil {
			return err
		}
	}
	return zipEntry(zw, "README.txt", 0o644, readmeTxt(mode, goos, goarch))
}

// renderNodeYaml 服务端渲染真实配置（字段名与 cmd/node 的 nodeConfig 对齐；
// 本机 API 监听字段为 local_listen——旧文档写 api_listen 是解码会静默忽略的
// 错名）。三个真实值均已过白名单，纯量无引号/控制字符，yaml 注入免疫。
func renderNodeYaml(v enrollVals) string {
	return `# SwarmMind 节点配置（hub 控制台生成；字段与 configs/node.example.yaml 对齐）
node_id: ` + v.NodeID + `
hub_url: ` + v.HubURL + `
register_token: ` + v.Token + `
hub_pubkey_fp: ""            # 留空 = 首次连接自动记录 hub 证书指纹（防中间人）
local_listen: 127.0.0.1:8445 # 本机回环 API（node 只监听 127.0.0.1，不给公网）
data_dir: data
concurrency: 2

# 能力（agents）段示例——这台机器有什么智能体就配什么，保存重启后自动注册到 hub：
# agents:
#   - name: my-agent
#     kind: cli
#     command: /usr/local/bin/my-agent   # 必须绝对路径
#     workdir: /home/user/work           # 必须绝对路径
#     timeout_sec: 600
#     description: 示例条目（照此格式增删；暂不配置也能上线，只是先不开放能力）
`
}

// unixInstallSh linux/darwin 安装脚本：装二进制与配置（node.yaml 含令牌一律
// chmod 0600）→ 探测 systemd（linux）生成单元并 enable --now，darwin 现场生成
// 用户级 launchd plist（ProgramArguments=绝对路径二进制+--config+node.yaml、
// WorkingDirectory=安装目录、KeepAlive、RunAtLoad）并 load；两者皆无时给前台
// 运行提示（不静默失败）。
func unixInstallSh(goos string) string {
	if goos == "darwin" {
		return `#!/bin/sh
# SwarmMind 节点安装脚本（macOS）：装到 ~/.swarmmind 并注册 launchd 用户服务
set -eu
cd "$(dirname "$0")"

	DIR="$HOME/.swarmmind"
PLIST=~/Library/LaunchAgents/com.swarmmind.node.plist # 无引号赋值，shell 展开 ~

chmod +x swarmmind-node
mkdir -p "$DIR" "$HOME/Library/LaunchAgents"
cp swarmmind-node "$DIR/swarmmind-node"
cp node.yaml "$DIR/node.yaml"
chmod 0600 "$DIR/node.yaml"

if command -v launchctl >/dev/null 2>&1; then
  cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.swarmmind.node</string>
  <key>ProgramArguments</key>
  <array>
    <string>$DIR/swarmmind-node</string>
    <string>--config</string>
    <string>$DIR/node.yaml</string>
  </array>
  <key>WorkingDirectory</key><string>$DIR</string>
  <key>KeepAlive</key><true/>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>$DIR/node.log</string>
  <key>StandardErrorPath</key><string>$DIR/node.log</string>
</dict>
</plist>
EOF
  launchctl unload "$PLIST" >/dev/null 2>&1 || true
  launchctl load "$PLIST"
  echo "已安装并启动。看状态：launchctl list | grep swarmmind"
  echo "日志：$DIR/node.log（launchctl 查不到输出时先前台验证：$DIR/swarmmind-node --config $DIR/node.yaml）"
else
  echo "未检测到 launchctl，请前台运行：" >&2
  echo "  $DIR/swarmmind-node --config $DIR/node.yaml" >&2
  exit 1
fi
`
	}
	return `#!/bin/sh
# SwarmMind 节点安装脚本（Linux）：装到 /opt/swarmmind 并注册 systemd 服务
set -eu
cd "$(dirname "$0")"

if [ "$(id -u)" -ne 0 ]; then
  echo "请用 sudo 运行：sudo sh install.sh" >&2
  exit 1
fi

chmod +x swarmmind-node
mkdir -p /opt/swarmmind
cp swarmmind-node /opt/swarmmind/swarmmind-node
cp node.yaml /opt/swarmmind/node.yaml
chmod 0755 /opt/swarmmind/swarmmind-node
chmod 0600 /opt/swarmmind/node.yaml

if command -v systemctl >/dev/null 2>&1 && [ -d /etc/systemd/system ]; then
  cat > /etc/systemd/system/swarmmind-node.service <<'EOF'
[Unit]
Description=SwarmMind node
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/swarmmind/swarmmind-node --config /opt/swarmmind/node.yaml
WorkingDirectory=/opt/swarmmind
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now swarmmind-node
  echo "已安装并启动。看状态：systemctl status swarmmind-node"
else
  echo "未检测到 systemd，请前台运行：" >&2
  echo "  /opt/swarmmind/swarmmind-node --config /opt/swarmmind/node.yaml" >&2
  exit 1
fi
`
}

// windowsInstallBat Windows 安装脚本：管理员校验 → sc create + sc start。
// binPath 含空格路径，sc 语法要求「binPath= 〈值〉」等号后带空格、值整体加引号。
func windowsInstallBat() string {
	return `@echo off
rem SwarmMind 节点安装脚本（Windows）：注册 Windows 服务并启动
cd /d "%~dp0"
net session >nul 2>&1
if errorlevel 1 (
  echo 请右键本脚本，选择「以管理员身份运行」。
  pause
  exit /b 1
)
sc create swarmmind-node binPath= "\"%~dp0swarmmind-node.exe\" --config \"%~dp0node.yaml\"" start= auto
sc start swarmmind-node
echo 已安装并启动。看状态：sc query swarmmind-node
pause
`
}

// dockerfile scratch 基座：node 外连 TLS 用指纹校验（InsecureSkipVerify + SPKI
// 指针核对），不读系统 CA 池，scratch 可直接跑；配置经 compose 挂载进
// /node.yaml（挂载覆盖镜像内同名层，改配置不必重建镜像）。
func dockerfile() string {
	return `FROM scratch
COPY swarmmind-node /swarmmind-node
ENTRYPOINT ["/swarmmind-node", "--config", "/node.yaml"]
`
}

// dockerCompose 挂载配置与数据卷，unless-stopped 常驻。
func dockerCompose() string {
	return `services:
  swarmmind-node:
    build: .
    image: swarmmind-node:latest
    restart: unless-stopped
    volumes:
      - ./node.yaml:/node.yaml:ro
      - ./data:/data
`
}

// readmeTxt 三行上手（解压 → 运行安装脚本 → 看状态），按平台给具体命令。
func readmeTxt(mode, goos, goarch string) string {
	var b strings.Builder
	if mode == "docker" {
		fmt.Fprintf(&b, "SwarmMind 节点部署包（Docker · linux/%s）\n\n", goarch)
		b.WriteString(`1. 解压：unzip 本包并进入目录（Windows 用右键「全部解压缩」）
2. 启动：docker build -t swarmmind-node . && docker compose up -d
3. 看状态：docker compose ps && docker compose logs -f

说明：node.yaml 已按这台节点填好真实值；改配置后 docker compose restart 即可。
node.yaml 含接入令牌（register_token），属敏感凭据：文件权限已收紧为 0600
（仅属主可读写），请勿转发明文、不要提交进代码仓库。
`)
		return b.String()
	}
	fmt.Fprintf(&b, "SwarmMind 节点部署包（%s/%s）\n\n", goos, goarch)
	switch goos {
	case "darwin":
		b.WriteString(`1. 解压：双击 zip（或 unzip swarmmind-node-*.zip）并进入目录
2. 安装：sh install.sh（写入 ~/.swarmmind 并注册 launchd 用户服务，免 sudo）
3. 看状态：launchctl list | grep swarmmind
`)
	case "windows":
		b.WriteString(`1. 解压：右键 zip「全部解压缩」并进入目录
2. 安装：右键 install.bat「以管理员身份运行」（注册 Windows 服务并启动）
3. 看状态：sc query swarmmind-node
`)
	default:
		b.WriteString(`1. 解压：unzip swarmmind-node-*.zip && cd swarmmind-node
2. 安装：sudo sh install.sh（装到 /opt/swarmmind 并注册 systemd 服务）
3. 看状态：systemctl status swarmmind-node
`)
	}
	b.WriteString(`
说明：node.yaml 已按这台节点填好真实值；智能体能力在 node.yaml 的 agents 段配置，
保存重启节点后自动注册到 hub。节点连上 hub 后即在控制台节点列表显示在线。
node.yaml 含接入令牌（register_token），属敏感凭据：文件权限已收紧为 0600
（仅属主可读写，安装脚本内另有 chmod 兜底），请勿转发明文、不要提交进代码仓库。`)
	return b.String()
}

// ---------- zip 工具 ----------

// zipEntry 写入内存条目（mode 走 zip 外部属性，保证 install.sh 落地可执行）。
func zipEntry(zw *zip.Writer, name string, mode fs.FileMode, data string) error {
	return zipEntryStream(zw, name, mode, strings.NewReader(data))
}

// zipEntryFromBytes 内存字节条目（内置预编译产物直入包）。
func zipEntryFromBytes(zw *zip.Writer, name string, mode fs.FileMode, data []byte) error {
	return zipEntryStream(zw, name, mode, bytes.NewReader(data))
}

// zipEntryFromFile 磁盘文件流式入包（二进制不整块进内存）。
func zipEntryFromFile(zw *zip.Writer, name string, mode fs.FileMode, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开编译产物 %s 失败: %w", path, err)
	}
	defer src.Close()
	return zipEntryStream(zw, name, mode, src)
}

// zipEntryStream 流式写单条目（mode 走 zip 外部属性）。
func zipEntryStream(zw *zip.Writer, name string, mode fs.FileMode, r io.Reader) error {
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	h.SetMode(mode)
	f, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	return err
}
