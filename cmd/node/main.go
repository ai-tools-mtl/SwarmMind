// node 进程入口（SPEC 二/五/六）：外连 hub（退避重连、TOFU/指纹校验）、启动账本
// 恢复（重调度/重发）、任务执行服务、本机回环 API（默认 127.0.0.1:8445，Loop 4）、
// 优雅关闭。node 不监听任何公网端口。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mtl802/swarmmind/internal/node/conn"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/localapi"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// agentProfile yaml 侧 ExecProfile 镜像（protocol 包保持纯协议层，不引入 yaml
// tag）。exposed 为指针：yaml 未写时默认 true（SPEC 4.9：接入开关默认开）。
type agentProfile struct {
	Name           string   `yaml:"name"`
	Kind           string   `yaml:"kind"`
	Command        string   `yaml:"command"`
	Workdir        string   `yaml:"workdir"`
	AgentArgs      []string `yaml:"agent_args"`
	TimeoutSec     int      `yaml:"timeout_sec"`
	AllowArgs      bool     `yaml:"allow_args"`
	AllowEnv       []string `yaml:"allow_env"`
	ConfigHome     string   `yaml:"config_home"`
	Description    string   `yaml:"description"`
	PermissionMode string   `yaml:"permission_mode"`
	Exposed        *bool    `yaml:"exposed"`
}

func (p agentProfile) toExecProfile() protocol.ExecProfile {
	exposed := true
	if p.Exposed != nil {
		exposed = *p.Exposed
	}
	kind := protocol.AgentKind(p.Kind)
	if kind == "" {
		kind = protocol.AgentKindCLI
	}
	return protocol.ExecProfile{
		Name: p.Name, Kind: kind, Command: p.Command, Workdir: p.Workdir,
		AgentArgs: p.AgentArgs, TimeoutSec: p.TimeoutSec, AllowArgs: p.AllowArgs,
		AllowEnv: p.AllowEnv, ConfigHome: p.ConfigHome, Description: p.Description,
		PermissionMode: p.PermissionMode, Exposed: exposed,
	}
}

// nodeConfig configs/node.yaml 结构（SPEC 五/六）。
type nodeConfig struct {
	NodeID        string            `yaml:"node_id"`
	HubURL        string            `yaml:"hub_url"`
	RegisterToken string            `yaml:"register_token"`
	HubPubkeyFP   string            `yaml:"hub_pubkey_fp"` // SPKI SHA-256；空则 TOFU 记录首连（Loop 4 已实现）
	LocalListen   string            `yaml:"local_listen"`  // 本地 API 监听（默认 127.0.0.1:8445，强制回环）
	LocalToken    string            `yaml:"local_token"`   // 本地 API 管理令牌（空则生成存 data_dir/local_token）
	HubAPIURL     string            `yaml:"hub_api_url"`   // hub 管理面（GET /agents 全网目录代理，可选）
	HubAPIToken   string            `yaml:"hub_api_token"` // hub 管理令牌（随 hub_api_url）
	Concurrency   int               `yaml:"concurrency"`
	DataDir       string            `yaml:"data_dir"`
	BaseEnv       map[string]string `yaml:"base_env"`
	Agents        []agentProfile    `yaml:"agents"`
}

func main() {
	cfgPath := flag.String("config", "configs/node.example.yaml", "配置文件路径（yaml）")
	dataDir := flag.String("data-dir", "", "覆盖配置中的 data_dir")
	flag.Parse()

	if err := run(*cfgPath, *dataDir); err != nil {
		log.Fatalf("[node] %v", err)
	}
}

func run(cfgPath, dataDirOverride string) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	if dataDirOverride != "" {
		cfg.DataDir = dataDirOverride
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.NodeID == "" || cfg.HubURL == "" || cfg.RegisterToken == "" {
		return errors.New("node_id / hub_url / register_token 均为必填")
	}
	// N2：node_id 有界（From 计入信封体积计量，超长身份会撑爆 register/结果帧）
	if len(cfg.NodeID) > protocol.MaxNodeIDLen {
		return fmt.Errorf("node_id 长度 %d 字节超过上限 %d", len(cfg.NodeID), protocol.MaxNodeIDLen)
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = protocol.DefaultConcurrency
	}
	if cfg.LocalListen == "" {
		cfg.LocalListen = "127.0.0.1:8445"
	}
	if err := localapi.ValidateListen(cfg.LocalListen); err != nil {
		return err // node 不监听任何公网端口（SPEC 二，代码强制）
	}
	if cfg.HubPubkeyFP != "" {
		log.Printf("[node] hub_pubkey_fp 已配置：强校验模式（SPKI SHA-256）")
	} else {
		log.Printf("[node] hub_pubkey_fp 未配置：TOFU 模式（首连记录 %s）", filepath.Join(cfg.DataDir, "trusted_hubs.json"))
	}
	profiles := make([]protocol.ExecProfile, 0, len(cfg.Agents))
	for _, ap := range cfg.Agents {
		p := ap.toExecProfile()
		if p.Name == "" || p.Command == "" || p.Workdir == "" {
			return fmt.Errorf("agent %q: name/command/workdir 均为必填", p.Name)
		}
		if !filepath.IsAbs(p.Command) || !filepath.IsAbs(p.Workdir) {
			return fmt.Errorf("agent %q: command/workdir 必须为绝对路径", p.Name)
		}
		if p.TimeoutSec <= 0 {
			return fmt.Errorf("agent %q: timeout_sec 必须 >0", p.Name)
		}
		profiles = append(profiles, p)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("创建 data_dir 失败: %w", err)
	}

	// 本机 agent 运行时状态（Loop 7）：agents-state.json 覆盖叠加 YAML 基线，
	// effective 视图同时供注册能力表与执行器装配——开关重启保持、三处同源。
	agentState, err := localapi.OpenAgentState(cfg.DataDir, profiles)
	if err != nil {
		return fmt.Errorf("打开 agent 状态失败: %w", err)
	}
	profiles = agentState.Effective()

	// 本机提交任务日志（Loop 7 我的任务列表）。
	taskLog, err := localapi.OpenTaskLog(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("打开任务日志失败: %w", err)
	}

	// 账本：data_dir flock（单实例）+ 启动恢复（SPEC 4.8）
	led, rec, err := ledger.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("打开账本失败: %w", err)
	}
	defer led.Close()
	if len(rec.Reschedule) > 0 {
		log.Printf("[node] 恢复 %d 个待执行任务", len(rec.Reschedule))
	}
	if len(rec.Resend) > 0 {
		log.Printf("[node] 恢复 %d 个待重发结果", len(rec.Resend))
	}

	// 执行器装配（base_env 干净基线，HOME=ConfigHome，默认 data_dir/home）；
	// ArtifactRoots 同源装配（产物取回路径解析基准，Loop 4）。
	runners := make(map[string]service.Runner, len(profiles))
	artifactRoots := make(map[string]string, len(profiles))
	defaultHome := filepath.Join(cfg.DataDir, "home")
	for _, p := range profiles {
		runners[p.Name] = runner.New(p, cfg.BaseEnv, defaultHome)
		artifactRoots[p.Name] = p.Workdir
	}

	// 外连客户端 + 任务服务（conn 实现 transport.Transport，membus 同型替换）。
	// C3：每次注册成功立即重发账本未确认结果（SPEC 4.5）+ 能力表同步（Loop 7，
	// 与 hub 注册表收敛）——先声明 service 变量，经闭包装配 OnRegistered（非阻塞、
	// 并发合并，不拖住连接生命周期）。TLS 身份校验（SPEC 二）：PubkeyFP 非空强校验，
	// 空则 TOFU 记录。注册能力表经 AgentsFunc 每次连接取 AgentState 运行时视图
	//（exposed 开关重启保持、重连不回退）。
	var svc *service.Service
	client := conn.NewClient(conn.Config{
		HubURL:     cfg.HubURL,
		NodeID:     cfg.NodeID,
		Token:      cfg.RegisterToken,
		AgentsFunc: agentState.Cards,
		PubkeyFP:   cfg.HubPubkeyFP,
		TrustStore: conn.OpenTrustStore(cfg.DataDir),
		OnRegistered: func() {
			if svc != nil {
				svc.ResendPendingAsync()
				agentState.Sync(svc.SendCardUpdate)
			}
		},
	})
	svc = service.New(service.Config{
		Ledger:        led,
		Runners:       runners,
		ArtifactRoots: artifactRoots,
		Concurrency:   cfg.Concurrency,
	}, client)

	// 本地回环 API（SPEC 六，Loop 4）：提交/查询经会话帧转发 + 能力目录；
	// 令牌为空则生成存 data_dir/local_token（0600），日志只报位置。
	localToken := cfg.LocalToken
	if localToken == "" {
		tok, err := ensureLocalToken(filepath.Join(cfg.DataDir, "local_token"))
		if err != nil {
			return fmt.Errorf("生成 local_token 失败: %w", err)
		}
		localToken = tok
		log.Printf("[node] 未配置 local_token：已生成随机令牌并存 %s（0600）", filepath.Join(cfg.DataDir, "local_token"))
	}
	localSrv := localapi.New(localapi.Config{
		NodeID:           cfg.NodeID,
		Service:          svc,
		Agents:           agentState,
		Ledger:           led,
		TaskLog:          taskLog,
		CardsReporter:    svc.SendCardUpdate,
		Online:           client.Online,
		Epoch:            client.Epoch,
		LastRegisteredAt: client.LastRegisteredAt,
		HubAPIURL:        cfg.HubAPIURL,
		HubAPIToken:      cfg.HubAPIToken,
		LocalToken:       localToken,
	})
	localLn, err := net.Listen("tcp", cfg.LocalListen)
	if err != nil {
		return fmt.Errorf("本地 API 监听 %s 失败: %w", cfg.LocalListen, err)
	}
	localHTTP := &http.Server{Handler: localSrv.Handler()}
	go func() {
		if err := localHTTP.Serve(localLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[node] 本地 API 监听异常退出: %v", err)
		}
	}()
	log.Printf("[node] 本地 API 已启动 %s（/api/v1/*，Bearer local_token）", cfg.LocalListen)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for _, t := range rec.Reschedule {
		svc.Execute(t) // 恢复的 accepted 任务交回本地调度（并发受信号量限制）
	}
	log.Printf("[node] 启动完成 node=%s hub=%s agents=%d concurrency=%d data=%s",
		cfg.NodeID, cfg.HubURL, len(profiles), cfg.Concurrency, cfg.DataDir)

	err = client.Run(ctx) // 退避重连循环，直至信号或 auth_failed（不重试）
	// 优雅关闭（SIGTERM/SIGINT 经上方 NotifyContext 触发，SPEC 4.8）：本地 API
	// 停收新请求 → 任务服务（取消在途任务 ctx → runner kill(-pgid) 整组终止
	// 子进程 → 在途任务以 needs_review/node_restarted_midway 落账收尾）→ 连接
	// （Run 返回即断）→ defer led.Close 释放 flock。子进程绝不成为孤儿跑完。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), closeBudget)
	defer cancel()
	_ = localHTTP.Shutdown(shutdownCtx)
	svc.Close()
	_ = client.Close()
	if errors.Is(err, conn.ErrAuthFailed) {
		return fmt.Errorf("register 鉴权失败（auth_failed 不重试，请核对 node_id 与 register_token）: %w", err)
	}
	return err
}

// closeBudget 本地 API 优雅关闭预算（调度节拍，非协议常量）。
const closeBudget = 5 * time.Second

// ensureLocalToken 生成 256bit 随机令牌；已有令牌文件则复用（重启不换令牌）。
func ensureLocalToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return string(b), nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw[:])
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, os.Rename(tmp, path)
}

// loadConfig 读取并解析 yaml 配置。
func loadConfig(path string) (*nodeConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	var cfg nodeConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败: %w", path, err)
	}
	return &cfg, nil
}
