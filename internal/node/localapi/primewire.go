// declare/rotate 统筹自动接线（t20261023-62a2，2026-10-23）：钦定/轮换成功后
// node 自动把 swarmmind MCP 配置合并写入被钦定 agent 影子 home（ExecProfile.
// ConfigHome，空则 data_dir/home——与 runner HOME 同口径）内的宿主配置文件，
// 「钦定即接线，零手贴」；探测不到已知宿主布局时降级为手动片段流程。
//
// 安全边界（SPEC 六.六豁免③）：prime_token 绝不经 agent 面分发；本文件全部
// 动作发生在 declare/rotate 这个主人人工操作的事务内（走主人授权），不越线。
// 影子 home 虽是 node 托管，但布局探测只认 zcode 一种（setting.json，单数），
// 未知宿主一律降级——宁可让人手动贴，不可猜布局乱写。绝不硬写、绝不创建
// 宿主目录。撤销统筹不清理已写入的配置：token 已即时失效，残留无害。
package localapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// primeBackupSuffix 接线写前备份后缀（<setting.json>.bak-prime；重复接线直接
// 覆盖旧备份，不堆积）。
const primeBackupSuffix = ".bak-prime"

// wirePrimeConfig 探测影子 home 内已知宿主布局并合并写入统筹 MCP 配置：
//   - zcode 布局：<home>/.zcode/cli/setting.json 存在且为合法 JSON → 命中；
//   - 合并：顶层 mcpServers.swarmmind = mcp（幂等，覆盖同键，其他键原样；
//     解码开 UseNumber——无关键的数值保持原字面量，不做 float64 往返换算）；
//   - 写盘：先备份为 <file>.bak-prime（直接覆盖旧备份）→ 临时文件 + rename
//     原子写，0600（文件内含 prime_token 凭证）。
//
// 降级（wired=false，不是错误）：文件/目录不存在、JSON 非法（含顶层 null 与
// 尾部多余内容）、顶层或 mcpServers 非对象、备份或写盘失败——原文件不动，绝不
// 创建宿主目录。err 可为 nil 或带说明（调用方仅按 wired 分流，err 不进应答）。
func wirePrimeConfig(home string, mcp map[string]any) (wired bool, target string, err error) {
	if home == "" {
		return false, "", errors.New("影子 home 为空（agent 未配置 config_home 且无 data_dir 兜底）")
	}
	path := filepath.Join(home, ".zcode", "cli", "setting.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "", nil // 未装 zcode / 布局未知：降级手动片段，非错误
	}
	if err != nil {
		return false, "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	doc := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return false, "", fmt.Errorf("%s 非合法 JSON，不硬写: %w", path, err)
	}
	if doc == nil {
		// R1（codex 审查）：文件内容 null 时 Decode 成功但 doc 置 nil map，随后
		// 赋值即 panic——此时 declare/rotate 已换发凭证，旧凭证失效、新凭证未
		// 正常展示。null 无安全合并点，降级不硬写。
		return false, "", fmt.Errorf("%s 顶层为 null，不硬写", path)
	}
	// R2（codex 审查）：验证首个值之后仅余空白到 EOF——`{} garbage`/`{} {}`
	// 会先成功解出对象再带尾巴，直接覆盖即破坏原文（非法 JSON 降级不硬写）。
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return false, "", fmt.Errorf("%s JSON 尾部有多余内容（仅容空白），不硬写", path)
	}
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		if _, exists := doc["mcpServers"]; exists {
			return false, "", fmt.Errorf("%s 的 mcpServers 非对象，不硬写", path)
		}
		servers = map[string]any{} // 键不存在：新建，不动其他顶层键
	}
	servers["swarmmind"] = mcp
	doc["mcpServers"] = servers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, "", fmt.Errorf("重编码 %s 失败: %w", path, err)
	}
	out = append(out, '\n')
	// 备份先于主文件落盘且失败即中止（不留无备份的改写）；rotate 场景备份内含
	// 旧 token——旧凭证此时已失效，残留无害（撤销同理，不清理）。
	if err := atomicWriteFile0600(path+primeBackupSuffix, raw); err != nil {
		return false, "", fmt.Errorf("写备份失败（主文件未动）: %w", err)
	}
	if err := atomicWriteFile0600(path, out); err != nil {
		return false, "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, "", fmt.Errorf("解析绝对路径失败: %w", err)
	}
	return true, abs, nil
}

// atomicWriteFile0600 临时文件 + rename 原子写（0600；不创建目录——目录存在
// 由调用方场景保证，接线场景主文件已在）。接线主文件（内含 prime_token）与
// 备份共用（CLAUDE.md 原子写纪律；独占临时文件防并发复用，fsync 防 rename
// 先于数据落盘）。
func atomicWriteFile0600(path string, content []byte) error {
	dir := filepath.Dir(path)
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
	if _, err := tmp.Write(content); err != nil {
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

// wirePrime declare/rotate 成功路径的接线入口（调用方持 muPrime——轮换与接线
// 同互斥段串行，杜绝「旧 token 后落盘盖掉新 token」的交错）：解析被钦定
// agent 的影子 home，命中已知宿主布局即合并写入 mcp（含最新 prime_token）。
// 占位符 command / agent 不在表 / home 不可解 / 布局未命中 → wired=false 降级
// 手动片段。
func (s *Server) wirePrime(agent string, mcp map[string]any) (wired bool, target string, err error) {
	// R4（Hana 裁决）：command 探测未命中（占位符）→ 一律降级手动片段，不写
	// 占位配置——落盘一个启动不了的 MCP 条目没有任何用处。
	if cmd, _ := mcp["command"].(string); cmd == mcpCommandPlaceholder {
		return false, "", errors.New("swarmmind-mcp 探测未命中（command 为占位符），不写占位配置")
	}
	home := s.agentShadowHome(agent)
	if home == "" {
		return false, "", nil
	}
	return wirePrimeConfig(home, mcp)
}

// agentShadowHome agent 影子 home（runner HOME 同口径：ConfigHome，空则
// data_dir/home）。agent 不在表、或 ConfigHome 与 DataDir 双空 → 空串
// （不可解，接线降级）。
func (s *Server) agentShadowHome(agent string) string {
	for _, p := range s.effectiveProfiles() {
		if p.Name != agent {
			continue
		}
		if p.ConfigHome != "" {
			return p.ConfigHome
		}
		if s.cfg.DataDir != "" {
			return filepath.Join(s.cfg.DataDir, "home")
		}
		return ""
	}
	return ""
}

// applyWiredFields declare/rotate 应答公共段：接线结果落 wired/wired_target
// 字段并返回该口径的接线说明（wired：已自动写入的路径 + 重连即生效；降级：
// 手动配给指引）。凭证「仅此一次展示」提示两口径通用，由调用方拼接。
func applyWiredFields(resp map[string]any, wired bool, target string) string {
	resp["wired"] = wired
	if wired {
		resp["wired_target"] = target
		return "统筹 MCP 连接已自动写入 " + target + "（无需手贴；agent 重连后生效）"
	}
	return "新凭证仅此一次展示，请立即配给统筹的 MCP 连接（SWARM_NODE_PRIME_TOKEN）"
}
