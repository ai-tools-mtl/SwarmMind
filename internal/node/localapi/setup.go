// setup 接入向导（方案 3.3，2026-10-22）：node.yaml 不存在的三态之一——仅起
// 本机控制台，页面顶部「接入中心端」向导完成接入：
//   1. 粘贴接入串（hub 控制台复制，swarmmind://join?hub=&node=&token=）；
//   2. parse：同源校验（protocol.ParseJoinString，3.6 下沉）→ 预览将写入的
//      node.yaml；
//   3. apply：写盘（写回启动时 --config 原路径，绝不写示例文件；data_dir 落盘前
//      绝对路径化，防 cwd 漂移双 data_dir 双 token）→ HTTP 应答先返回 → 装配方
//      延时优雅退出，launchd/systemd KeepAlive 拉起进入组网模式。
//
// apply 落盘分两路（功能性修复 1，2026-10-22）：盘上无文件（setup 新装）生成
// 完整模板 renderSetupYaml；已有文件（standalone/组网重接入、换 hub）走
// yaml.Node round-trip 合并 mergeJoinYAML——仅更新 hub_url/node_id/register_token
// 三键 + local_listen/data_dir 规范化，agents/prime_agent/自定义 local_token/
// base_env/注释全保留（接入不得覆盖用户手编配置）。
//
// disconnect（矩阵 #9「断开组网」的服务端）：组网控制台的断开入口——yaml.Node
// 编辑仅清 hub_url（注释与未知字段保留），应答后延时重启回 standalone。
//
// 鉴权：setup 端点同样验 local_token（免鉴权写盘 = agents.command 注入 RCE 面，
// zcode 审查 #9）；页面明示 token 位置（data_dir/local_token）。
package localapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// SetupOps setup 向导装配方（cmd/node 接线；nil = setup 端点 503）。
type SetupOps struct {
	// ConfigPath 写盘目标 = 启动时 --config 原路径（绝不写示例文件）。
	ConfigPath string
	// DataDir 当前生效 data_dir（写盘前绝对路径化）。
	DataDir string
	// LocalListen 预览/落盘的本地监听（setup 模式默认 127.0.0.1:8445）。
	LocalListen string
	// Concurrency 写入的执行并发（setup 模式默认 protocol.DefaultConcurrency）。
	Concurrency int
	// OnApplied 落盘成功后回调（装配方延时优雅退出，保活拉起新形态；裸前台
	// 跑打印重启命令）。nil = 仅落盘不退出。
	OnApplied func()
}

// setupRequest 向导请求体（parse/apply 共用）。
type setupRequest struct {
	JoinString string `json:"join_string"`
}

// handleSetupParse POST /api/v1/local/setup/parse：解析接入串 → 三元组 + 预览
// yaml（无副作用，可反复试）。预览与 apply 同源 renderOrMergeSetupYaml——已有
// 配置时预览即合并结果（所见即所存）。非法输入 → 400 人话原因（矩阵 #11）。
func (s *Server) handleSetupParse(w http.ResponseWriter, r *http.Request) {
	ops, req, ok := s.setupPreflight(w, r)
	if !ok {
		return
	}
	v, err := protocol.ParseJoinString(req.JoinString)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_join_string", err.Error())
		return
	}
	writeConfigMu.Lock()
	preview, err := renderOrMergeSetupYaml(v, ops) // 锁内读盘：预览 coherent 快照
	writeConfigMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "渲染 node.yaml 预览失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hub_url": v.HubURL, "node_id": v.NodeID, "yaml_preview": preview,
		"merged": fileExists(ops.ConfigPath),
	})
}

// handleSetupApply POST /api/v1/local/setup/apply：校验 → 写盘（原路径 + 原子
// 替换 + 0600）→ 回调装配方（延时优雅退出）→ 应答。写盘失败不退出、返回 500。
// 已有配置走合并（功能性修复 1）：读-合并-写全程持 writeConfigMu（与 declare/
// PUT config/disconnect 的读改写事务互斥——merge 引入读文件步骤后不再是无状态
// 渲染，防并发事务各自读旧文件互相覆盖）。
func (s *Server) handleSetupApply(w http.ResponseWriter, r *http.Request) {
	ops, req, ok := s.setupPreflight(w, r)
	if !ok {
		return
	}
	v, err := protocol.ParseJoinString(req.JoinString)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_join_string", err.Error())
		return
	}
	writeConfigMu.Lock()
	content, err := renderOrMergeSetupYaml(v, ops)
	if err == nil {
		err = writeConfigFileLocked(ops.ConfigPath, content)
	}
	writeConfigMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", "node.yaml 写盘失败（未改动原文件则可重试）: "+err.Error())
		return
	}
	if ops.OnApplied != nil {
		ops.OnApplied() // 先排定延时退出，再应答——HTTP 应答先返回（方案 3.3）
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"written": ops.ConfigPath, "node_id": v.NodeID, "hub_url": v.HubURL,
		"note": "已写入 node.yaml，node 将在数秒后自动重启进入组网模式（保活拉起）；若为裸前台运行，请手动重启进程后刷新本页",
	})
}

// handleSetupDisconnect POST /api/v1/local/setup/disconnect：断开组网（矩阵 #9
// 服务端）——yaml.Node 编辑仅清 hub_url，其余（agents/base_env/注释）原样保留；
// 写盘成功 → 回调延时退出 → 重启后 hub_url 为空 = standalone。
func (s *Server) handleSetupDisconnect(w http.ResponseWriter, r *http.Request) {
	ops, _, ok := s.setupPreflight(w, r)
	if !ok {
		return
	}
	if err := clearHubURL(ops.ConfigPath); err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", "断开组网写盘失败（未改动原文件）: "+err.Error())
		return
	}
	if ops.OnApplied != nil {
		ops.OnApplied()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"written": ops.ConfigPath,
		"note":    "已清空 hub_url，node 将在数秒后自动重启进入 standalone（本机自治）模式",
	})
}

// setupPreflight 公共前置：装配存在性与请求体解析。请求体容忍空 body 与 {}
//（功能性修复 5：断开组网前端/脚本可发空体；parse/apply 的 join_string 缺失
// 由 ParseJoinString 给出针对性 400），非空则必须是合法 JSON。
func (s *Server) setupPreflight(w http.ResponseWriter, r *http.Request) (*SetupOps, setupRequest, bool) {
	if s.cfg.SetupOps == nil {
		writeErr(w, http.StatusServiceUnavailable, "setup_disabled", "setup 向导未装配（装配方未接线 SetupOps）")
		return nil, setupRequest{}, false
	}
	var req setupRequest
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体读取失败（上限 64KiB）: "+err.Error())
		return nil, setupRequest{}, false
	}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", `请求体须为 JSON {"join_string":"swarmmind://join?…"}`)
			return nil, setupRequest{}, false
		}
	}
	return s.cfg.SetupOps, req, true
}

// fileExists 路径存在性（parse 应答 merged 标记用；读错误按不存在呈现——预览
// 场景不因权限问题误报「将合并」）。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// renderOrMergeSetupYaml parse 预览与 apply 落盘共用的内容产出（功能性修复 1）：
// 盘上无文件（setup 新装）→ 完整模板 renderSetupYaml；已有文件 → round-trip
// 合并 mergeJoinYAML（仅三键 + 两键规范化，其余全保留）。读失败（权限等）作为
// 错误上抛——绝不覆盖读不到的文件。须持 writeConfigMu 调用（apply 路径锁内
// 读-合-写；parse 路径锁内取 coherent 快照）。
func renderOrMergeSetupYaml(v protocol.JoinValues, ops *SetupOps) (string, error) {
	raw, err := os.ReadFile(ops.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return renderSetupYaml(v, ops)
	}
	if err != nil {
		return "", fmt.Errorf("读取既有 node.yaml 失败（未改动原文件）: %w", err)
	}
	content, err := mergeJoinYAML(raw, v, ops)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// mergeJoinYAML 既有配置的接入合并（功能性修复 1，2026-10-22）：node.yaml 是
// 用户手编文件，重接入/换 hub 不再整文件重生成——yaml.Node round-trip 仅更新
// hub_url/node_id/register_token 三键（已有键原位换值、键与值的注释保留、缺失
// 则追加）+ local_listen/data_dir 规范化（缺失或空写默认；data_dir 相对路径
// 落盘前绝对路径化，方案 3.3），agents/prime_agent/自定义 local_token/base_env/
// 注释/未知字段全保留。解析失败/顶层非映射返回错误（调用方拒写，原文件不动）。
func mergeJoinYAML(raw []byte, v protocol.JoinValues, ops *SetupOps) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 node.yaml 失败（不动原文件，防覆盖手编配置）: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("node.yaml 顶层不是键值映射，无法安全编辑")
	}
	root := doc.Content[0]
	setupSetKey(root, "node_id", v.NodeID, "!!str")
	setupSetKey(root, "hub_url", v.HubURL, "!!str")
	setupSetKey(root, "register_token", v.Token, "!!str")
	// 规范化两键：local_listen 缺失/空 → 默认回环；data_dir 缺失/空 → 默认并
	// 绝对路径化；已有非空 data_dir 仅在相对路径时绝对化（已有绝对路径不动）。
	if setupKeyString(root, "local_listen") == "" {
		listen := ops.LocalListen
		if listen == "" {
			listen = "127.0.0.1:8445"
		}
		setupSetKey(root, "local_listen", listen, "!!str")
	}
	dataDir := setupKeyString(root, "data_dir")
	if dataDir == "" {
		dataDir = ops.DataDir
		if dataDir == "" {
			dataDir = "data"
		}
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("data_dir 绝对路径化失败: %w", err)
	}
	setupSetKey(root, "data_dir", abs, "!!str")
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("重编码 node.yaml 失败: %w", err)
	}
	return out, nil
}

// setupSetKey 顶层键更新/追加（mergeJoinYAML 专用）：已有键原位换值（键节点与
// 注释保留；既有值为别名等非纯量节点时整体替换，防悬空别名引用——setPrimeAgentYAML
// 修复 4 同型）；键不存在追加到映射尾部。
func setupSetKey(root *yaml.Node, key, val, tag string) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			old := root.Content[i+1]
			nv := &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: val}
			nv.HeadComment = old.HeadComment
			nv.LineComment = old.LineComment
			nv.FootComment = old.FootComment
			root.Content[i+1] = nv
			return
		}
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: val})
}

// setupKeyString 读顶层标量键文本（缺失/非纯量/引号空串 → ""；引号风格值按
// 字面返回——mergeJoinYAML 仅用于 local_listen/data_dir 的「是否需要规范化」
// 判定，风格前缀不影响空判定与 IsAbs 判定的正确性）。
func setupKeyString(root *yaml.Node, key string) string {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		n := root.Content[i+1]
		if n.Kind != yaml.ScalarNode {
			return ""
		}
		switch n.Value {
		case "", "''", `""`:
			return ""
		}
		return n.Value
	}
	return ""
}

// renderSetupYaml 渲染将写入的 node.yaml（agents 段留空——接入后在网页/手编添加；
// data_dir 落盘前绝对路径化，防 cwd 漂移导致双 data_dir 双 token，方案 3.3）。
// 字段名与 cmd/node 的 nodeConfig 对齐。
//
// 经 yaml.Node 编码器写出（返工 #10）：三元组值不再字符串拼接——含特殊字符时
// 由编码器加引号转义，杜绝注入面；data_dir 绝对化失败作为错误向上传播（调用方
// 返 500，不静默写相对路径）。
func renderSetupYaml(v protocol.JoinValues, ops *SetupOps) (string, error) {
	listen := ops.LocalListen
	if listen == "" {
		listen = "127.0.0.1:8445"
	}
	conc := ops.Concurrency
	if conc <= 0 {
		conc = protocol.DefaultConcurrency
	}
	dataDir := ops.DataDir
	if dataDir == "" {
		dataDir = "data"
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("data_dir 绝对路径化失败: %w", err)
	}
	root := &yaml.Node{Kind: yaml.MappingNode}
	add := func(key, val, tag, comment string) {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: val, LineComment: comment},
		)
	}
	add("node_id", v.NodeID, "!!str", "")
	add("hub_url", v.HubURL, "!!str", "")
	add("register_token", v.Token, "!!str", "")
	add("hub_pubkey_fp", "", "!!str", "留空 = 首次连接自动记录 hub 证书指纹（防中间人）")
	add("local_listen", listen, "!!str", "本机回环 API（node 只监听 127.0.0.1，不给公网）")
	add("data_dir", abs, "!!str", "")
	add("concurrency", strconv.Itoa(conc), "!!int", "")
	add("prime_agent", "", "!!str", "钦定本机唯一总理（agent 名）；空 = 编排接口未激活（403）")
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("node.yaml 编码失败: %w", err)
	}
	return string(out) + agentsHintComment, nil
}

// agentsHintComment 落盘文件尾部的 agents 段示例（纯注释，yaml 语义无关；
// 编码器不产出尾注，故拼接在编码结果之后）。
const agentsHintComment = `
# 能力（agents）段示例——这台机器有什么智能体就配什么，保存重启后自动注册到 hub：
# agents:
#   - name: my-agent
#     kind: cli
#     command: /usr/local/bin/my-agent   # 必须绝对路径
#     workdir: /home/user/work           # 必须绝对路径
#     timeout_sec: 600
#     description: 示例条目（照此格式增删；暂不配置也能上线，只是先不开放能力）
`

// writeConfigMu 配置读改写事务锁（返工 #5 立锁，安全审查修复 3 扩围）：凡共享
// node.yaml 的「读文件→改写→写盘→内存生效」全程在同一临界区内完成（setup apply
// 落盘、disconnect 清 hub_url、declare 写 prime_agent、PUT config/reload 整表
// 提交），杜绝并发事务各自读旧文件互相覆盖。锁内禁网络操作（CardsReporter 上报
// 在锁外）；锁序 muPrime→writeConfigMu→muCfg（包内无反向路径）。
var writeConfigMu sync.Mutex

// writeConfigFile 原子写配置文件（串行提交包装，见 writeConfigFileLocked）。
func writeConfigFile(path, content string) error {
	writeConfigMu.Lock()
	defer writeConfigMu.Unlock()
	return writeConfigFileLocked(path, content)
}

// writeConfigFileLocked 原子写（调用方须持 writeConfigMu）：独占临时文件
// （os.CreateTemp 同目录唯一名——固定 .tmp 名会在并发/残留场景被第二个写方
// 复用，且已存在临时文件的权限不被 WriteFile 收紧，返工 #5）+ fsync + rename；
// 权限显式 0600（内嵌 register_token 凭据，与 enroll 部署包同口径）。
func writeConfigFileLocked(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后文件已不存在，Remove 报错无害
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("收紧临时文件权限失败: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("刷盘失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename 落盘失败: %w", err)
	}
	return nil
}

// clearHubURL yaml.Node 编辑既有配置：仅把顶层 hub_url 置空，注释与未知字段
// 原样保留（node.yaml 是用户手编文件，不整文件重 marshal——3.5 round-trip 的
// 最小子集）。文件不存在 / 顶层非映射 / hub_url 本就为空：分别返回明确错误或
// 幂等成功。整段读改写持写盘锁（与 apply 串行，返工 #5）。
func clearHubURL(path string) error {
	writeConfigMu.Lock()
	defer writeConfigMu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("配置文件不存在（当前已是 setup/standalone 形态）")
		}
		return fmt.Errorf("读取配置失败: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return fmt.Errorf("解析配置失败（不动原文件，防覆盖手编配置）: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return errors.New("配置顶层不是键值映射，无法安全编辑")
	}
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "hub_url" {
			if v := m.Content[i+1].Value; v == "" || v == "''" || v == `""` {
				return nil // 已断开：幂等成功
			}
			m.Content[i+1].Value = ""
			m.Content[i+1].Tag = "!!null"
			m.Content[i+1].Style = 0
			out, err := yaml.Marshal(&root)
			if err != nil {
				return fmt.Errorf("重编码配置失败: %w", err)
			}
			return writeConfigFileLocked(path, string(out))
		}
	}
	return errors.New("配置中无 hub_url 字段（当前已是 standalone 形态）")
}
