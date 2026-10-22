// setup 接入向导（方案 3.3，2026-10-22）：node.yaml 不存在的三态之一——仅起
// 本机控制台，页面顶部「接入中心端」向导完成接入：
//   1. 粘贴接入串（hub 控制台复制，swarmmind://join?hub=&node=&token=）；
//   2. parse：同源校验（protocol.ParseJoinString，3.6 下沉）→ 预览将写入的
//      node.yaml；
//   3. apply：写盘（写回启动时 --config 原路径，绝不写示例文件；data_dir 落盘前
//      绝对路径化，防 cwd 漂移双 data_dir 双 token）→ HTTP 应答先返回 → 装配方
//      延时优雅退出，launchd/systemd KeepAlive 拉起进入组网模式。
//
// disconnect（矩阵 #9「断开组网」的服务端）：组网控制台的断开入口——yaml.Node
// 编辑仅清 hub_url（注释与未知字段保留），应答后延时重启回 standalone。
//
// 鉴权：setup 端点同样验 local_token（免鉴权写盘 = agents.command 注入 RCE 面，
// zcode 审查 #9）；页面明示 token 位置（data_dir/local_token）。
package localapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

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
// yaml（无副作用，可反复试）。非法输入 → 400 人话原因（矩阵 #11）。
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
	writeJSON(w, http.StatusOK, map[string]any{
		"hub_url": v.HubURL, "node_id": v.NodeID,
		"yaml_preview": renderSetupYaml(v, ops),
	})
}

// handleSetupApply POST /api/v1/local/setup/apply：校验 → 写盘（原路径 + 原子
// 替换 + 0600）→ 回调装配方（延时优雅退出）→ 应答。写盘失败不退出、返回 500。
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
	if err := writeConfigFile(ops.ConfigPath, renderSetupYaml(v, ops)); err != nil {
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

// setupPreflight 公共前置：装配存在性与请求体解析。
func (s *Server) setupPreflight(w http.ResponseWriter, r *http.Request) (*SetupOps, setupRequest, bool) {
	if s.cfg.SetupOps == nil {
		writeErr(w, http.StatusServiceUnavailable, "setup_disabled", "setup 向导未装配（仅 node.yaml 不存在的首启模式提供）")
		return nil, setupRequest{}, false
	}
	var req setupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", `请求体须为 JSON {"join_string":"swarmmind://join?…"}`)
		return nil, setupRequest{}, false
	}
	return s.cfg.SetupOps, req, true
}

// renderSetupYaml 渲染将写入的 node.yaml（agents 段留空——接入后在网页/手编添加；
// data_dir 绝对路径化）。字段名与 cmd/node 的 nodeConfig 对齐。
func renderSetupYaml(v protocol.JoinValues, ops *SetupOps) string {
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
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs // 防 cwd 漂移导致双 data_dir 双 token（方案 3.3）
	}
	return `# SwarmMind 节点配置（本机控制台接入向导写入；字段与 configs/node.example.yaml 对齐）
node_id: ` + v.NodeID + `
hub_url: ` + v.HubURL + `
register_token: ` + v.Token + `
hub_pubkey_fp: ""            # 留空 = 首次连接自动记录 hub 证书指纹（防中间人）
local_listen: ` + listen + ` # 本机回环 API（node 只监听 127.0.0.1，不给公网）
data_dir: ` + dataDir + `
concurrency: ` + fmt.Sprint(conc) + `
prime_agent: ""              # 钦定本机唯一总理（agent 名）；空 = 编排接口未激活（403）

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

// writeConfigFile 原子写配置文件（临时文件 + rename + 0600：内嵌 register_token
// 凭据，与 enroll 部署包同口径）。
func writeConfigFile(path, content string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建配置目录失败: %w", err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	return os.Rename(tmp, path)
}

// clearHubURL yaml.Node 编辑既有配置：仅把顶层 hub_url 置空，注释与未知字段
// 原样保留（node.yaml 是用户手编文件，不整文件重 marshal——3.5 round-trip 的
// 最小子集）。文件不存在 / 顶层非映射 / hub_url 本就为空：分别返回明确错误或
// 幂等成功。
func clearHubURL(path string) error {
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
			return writeConfigFile(path, string(out))
		}
	}
	return errors.New("配置中无 hub_url 字段（当前已是 standalone 形态）")
}
