// node 进程入口（SPEC 二/五/六 + 方案 node-standalone 三态，2026-10-22 返工）：
// 形态状态机（方案 3.1）——node.yaml 不存在 → setup（仅本机控制台 + 接入向导，
// 不连 hub）；存在但 hub_url 空 → standalone（本地直执行全功能，不连 hub）；
// 非空 → 组网（外连 hub：退避重连、TOFU/指纹校验）。yaml 解析失败报错退出，
// 绝不静默进 setup（防向导覆盖用户手编配置掩盖笔误）。
// 三态共用：账本启动恢复（重调度/重发）、任务执行服务、本机回环 API（默认
// 127.0.0.1:8445，Loop 4）、总理凭证（方案 3.2.1）、优雅关闭。node 不监听任何
// 公网端口。
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
	"github.com/mtl802/swarmmind/internal/transport"
)

// nodeConfig configs/node.yaml 结构（SPEC 五/六 + 方案 3.2.1 prime_agent）。
// agents 条目用 localapi.AgentProfile（yaml 镜像自 cmd/node 迁入共享——方案 3.5
// 配置编辑端点与装配方同源解析，protocol 包保持纯协议层）。
type nodeConfig struct {
	NodeID        string                   `yaml:"node_id"`
	HubURL        string                   `yaml:"hub_url"` // 形态开关：空 = standalone（方案 3.1）
	RegisterToken string                   `yaml:"register_token"`
	HubPubkeyFP   string                   `yaml:"hub_pubkey_fp"` // SPKI SHA-256；空则 TOFU 记录首连（Loop 4 已实现）
	LocalListen   string                   `yaml:"local_listen"`  // 本地 API 监听（默认 127.0.0.1:8445，强制回环）
	LocalToken    string                   `yaml:"local_token"`   // 本地 API 管理令牌（空则生成存 data_dir/local_token）
	PrimeAgent    string                   `yaml:"prime_agent"`   // 本机唯一总理声明（仅展示；prime_token 存 data_dir/prime_token，方案 3.2.1）
	Concurrency   int                      `yaml:"concurrency"`
	DataDir       string                   `yaml:"data_dir"`
	BaseEnv       map[string]string        `yaml:"base_env"`
	Agents        []localapi.AgentProfile  `yaml:"agents"`
}

func main() {
	cfgPath := flag.String("config", "configs/node.example.yaml", "配置文件路径（yaml；不存在则进 setup 模式）")
	dataDir := flag.String("data-dir", "", "覆盖配置中的 data_dir")
	flag.Parse()

	if err := run(*cfgPath, *dataDir); err != nil {
		log.Fatalf("[node] %v", err)
	}
}

// setupRestartDelay 向导落盘后延时优雅退出的预算：留出 HTTP 应答送达用户浏览器的
// 时间（方案 3.3「应答先返回 → 延时退出」），由 launchd/systemd KeepAlive 拉起。
const setupRestartDelay = 1500 * time.Millisecond

func run(cfgPath, dataDirOverride string) error {
	// 形态三态判定（方案 3.1，返工 #2）：读失败分「不存在」与「损坏」两种——
	// 只有前者进 setup，损坏（含 yaml 解析失败）一律报错退出。
	cfg, err := loadConfig(cfgPath)
	mode := localapi.ModeNetworked
	switch {
	case errors.Is(err, os.ErrNotExist):
		mode = localapi.ModeSetup
		cfg = &nodeConfig{}
		log.Printf("[node] 配置 %s 不存在：进入 setup 模式（仅本机控制台 + 接入向导，不连 hub）", cfgPath)
	case err != nil:
		return err // 读失败（无权限等）/ yaml 解析失败：报错退出，不静默进 setup（方案 3.1/矩阵 #17）
	case cfg.HubURL == "":
		mode = localapi.ModeStandalone
		log.Printf("[node] hub_url 为空：standalone 模式（本机自治全功能，不连 hub；离线派单照常）")
	}

	if dataDirOverride != "" {
		cfg.DataDir = dataDirOverride
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
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
	// 身份与连接必填项按形态校验：setup 无配置不校验（向导落盘后重启获得身份）；
	// standalone 仅 node_id（hub_url/register_token 不连 hub 即无意义）；组网三元组齐全。
	switch mode {
	case localapi.ModeSetup:
		// 无身份：控制台以空 node_id 呈现，等向导写入
	case localapi.ModeStandalone:
		if cfg.NodeID == "" {
			return errors.New("standalone 模式（hub_url 为空）仍须配置 node_id")
		}
	default:
		if cfg.NodeID == "" || cfg.HubURL == "" || cfg.RegisterToken == "" {
			return errors.New("node_id / hub_url / register_token 均为必填")
		}
	}
	// N2：node_id 有界（From 计入信封体积计量，超长身份会撑爆 register/结果帧）
	if cfg.NodeID != "" && len(cfg.NodeID) > protocol.MaxNodeIDLen {
		return fmt.Errorf("node_id 长度 %d 字节超过上限 %d", len(cfg.NodeID), protocol.MaxNodeIDLen)
	}
	if mode == localapi.ModeNetworked {
		if cfg.HubPubkeyFP != "" {
			log.Printf("[node] hub_pubkey_fp 已配置：强校验模式（SPKI SHA-256）")
		} else {
			log.Printf("[node] hub_pubkey_fp 未配置：TOFU 模式（首连记录 %s）", filepath.Join(cfg.DataDir, "trusted_hubs.json"))
		}
	}
	profiles := make([]protocol.ExecProfile, 0, len(cfg.Agents))
	for _, ap := range cfg.Agents {
		p := ap.ToExecProfile()
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
	// ArtifactRoots 同源装配（产物取回路径解析基准，Loop 4）。装配逻辑抽
	// buildExecutors（方案 3.5 可复用）：启动与热重载回调同源，杜绝两处漂移。
	defaultHome := filepath.Join(cfg.DataDir, "home")
	runners, artifactRoots := buildExecutors(profiles, cfg.BaseEnv, defaultHome)

	// 外连客户端仅组网态装配（方案 3.1：setup/standalone 不连 hub）。service
	// 三态都需要一个 transport：无 hub 端点的孤端点充当「本机黑洞汇」——出站帧
	// 一律 transport.ErrNoEndpoint（service 按良性静默），历史 hub 条目的迟到重发
	// 不出本机；本地任务本就被 origin 堵点拦在发送之前（方案 3.2）。
	var svc *service.Service
	var client *conn.Client
	var tr transport.Transport
	if mode == localapi.ModeNetworked {
		// C3：每次注册成功立即重发账本未确认结果（SPEC 4.5）+ 能力表同步（Loop 7，
		// 与 hub 注册表收敛）——先声明 service 变量，经闭包装配 OnRegistered（非阻塞、
		// 并发合并，不拖住连接生命周期）。TLS 身份校验（SPEC 二）：PubkeyFP 非空强校验，
		// 空则 TOFU 记录。注册能力表经 AgentsFunc 每次连接取 AgentState 运行时视图
		//（exposed 开关重启保持、重连不回退）。
		client = conn.NewClient(conn.Config{
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
		tr = client
	} else {
		tr = transport.NewBus().Endpoint("node-local")
	}
	svc = service.New(service.Config{
		Ledger:        led,
		Runners:       runners,
		ArtifactRoots: artifactRoots,
		Concurrency:   cfg.Concurrency,
	}, tr)

	// 本机凭证（SPEC 六 + 方案 3.2.1，返工 #1）：local_token 管只读+管理面，
	// prime_token 管编排；均存 data_dir（0600、独占临时文件原子写、重启复用）。
	// prime_token 恒生成——未钦定总理时同样生成但不分发，编排接口保持 403
	//（矩阵 #15）；node.yaml 的 prime_agent 仅声明展示。
	localTokenPath := filepath.Join(cfg.DataDir, "local_token")
	localToken := cfg.LocalToken
	if localToken == "" {
		tok, err := ensureTokenFile(localTokenPath)
		if err != nil {
			return fmt.Errorf("生成 local_token 失败: %w", err)
		}
		localToken = tok
		log.Printf("[node] 未配置 local_token：已生成随机令牌并存 %s（0600）", localTokenPath)
	}
	primeTokenPath := filepath.Join(cfg.DataDir, "prime_token")
	primeTok, err := ensureTokenFile(primeTokenPath)
	if err != nil {
		return fmt.Errorf("生成 prime_token 失败: %w", err)
	}
	if cfg.PrimeAgent == "" {
		log.Printf("[node] 尚未钦定总理（prime_agent 未声明）：prime_token 已生成于 %s 但不分发，编排接口保持 403", primeTokenPath)
	} else {
		log.Printf("[node] 本机总理：%s（编排凭证 %s，0600）", cfg.PrimeAgent, primeTokenPath)
	}
	// 轮换回调（方案 3.2.1 一键轮换）：重生成 + 原子写盘，返回新凭证；内存重载
	// 由 localapi 在同一 muPrime 互斥段内完成——生成→写盘→更新不交错（返工 #4）。
	rotatePrime := func() (string, error) {
		tok, err := newToken()
		if err != nil {
			return "", err
		}
		return tok, writeTokenFile(primeTokenPath, tok)
	}

	// 延时优雅退出（方案 3.3/3.5「应答先返回 → 延时退出」共用时序）：留出 HTTP
	// 应答送达用户浏览器的时间（setupRestartDelay），保活拉起进入新形态/新配置；
	// 裸前台跑在日志里给出重启命令。
	gracefulExit := func(msg string) {
		go func() {
			time.Sleep(setupRestartDelay)
			log.Printf("[node] %s（保活拉起后生效）；裸前台请手动重启: %s -config %s", msg, os.Args[0], cfgPath)
			stop()
		}()
	}

	// 本地回环 API（SPEC 六，Loop 4 + 方案 3.2/3.3/3.4/3.5）：本地直执行提交/
	// 账本视图直出 + 管理面 + 总理凭证 + setup 向导（三态均装配 SetupOps）+
	// 产物本机直取（ArtifactFetch）+ 能力配置编辑与热重载（ConfigOps）。
	localCfg := localapi.Config{
		NodeID:     cfg.NodeID,
		Mode:       mode,
		Setup:      mode == localapi.ModeSetup,
		Service:    svc,
		Agents:     agentState,
		Ledger:     led,
		TaskLog:    taskLog,
		LocalToken: localToken,
		PrimeToken: primeTok,
		PrimeAgent: cfg.PrimeAgent,
		RotatePrime: rotatePrime,
		ArtifactFetch: svc.FetchArtifactLocal,
		ConfigOps: &localapi.ConfigOps{
			ConfigPath: cfgPath, // 读改写启动时 --config 原路径（绝不写示例文件）
			ReloadExecutors: func(profiles []protocol.ExecProfile) error {
				// 热重载（方案 3.5）：与启动装配同源 buildExecutors + 换表
				//（在途任务持旧 runner 跑完不受影响）。必须异步安全——localapi
				// 在请求 goroutine 内调用，这里只做同步换表，不碰执行调度。
				runners, roots := buildExecutors(profiles, cfg.BaseEnv, defaultHome)
				svc.SwapExecutors(runners, roots)
				return nil
			},
			Restart:    func() { gracefulExit("已请求应用并重启，进程退出以加载新配置") },
			Supervised: detectSupervisor(),
			RestartCmd: fmt.Sprintf("%s -config %s", os.Args[0], cfgPath),
		},
		SetupOps: &localapi.SetupOps{
			ConfigPath:  cfgPath, // 写回启动时 --config 原路径（绝不写示例文件）
			DataDir:     cfg.DataDir,
			LocalListen: cfg.LocalListen,
			Concurrency: cfg.Concurrency,
			OnApplied: func() {
				// 落盘成功 → 延时优雅退出（HTTP 应答先返回），保活拉起进入新形态。
				gracefulExit("配置已写入，进程退出以切换形态")
			},
		},
	}
	if mode == localapi.ModeNetworked {
		localCfg.CardsReporter = svc.SendCardUpdate
		localCfg.Online = client.Online
		localCfg.Epoch = client.Epoch
		localCfg.LastRegisteredAt = client.LastRegisteredAt
	}
	localSrv := localapi.New(localCfg)
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

	for _, t := range rec.Reschedule {
		svc.Execute(t) // 恢复的 accepted 任务交回本地调度（并发受信号量限制；三态一致）
	}
	switch mode {
	case localapi.ModeSetup:
		log.Printf("[node] setup 就绪：控制台 http://%s（接入向导）；本机管理令牌见 %s", cfg.LocalListen, localTokenPath)
	case localapi.ModeStandalone:
		log.Printf("[node] standalone 启动完成 node=%s agents=%d concurrency=%d data=%s（不连 hub）",
			cfg.NodeID, len(profiles), cfg.Concurrency, cfg.DataDir)
	default:
		log.Printf("[node] 启动完成 node=%s hub=%s agents=%d concurrency=%d data=%s",
			cfg.NodeID, cfg.HubURL, len(profiles), cfg.Concurrency, cfg.DataDir)
	}

	var runErr error
	if mode == localapi.ModeNetworked {
		runErr = client.Run(ctx) // 退避重连循环，直至信号或 auth_failed（不重试）
	} else {
		<-ctx.Done() // setup/standalone：常驻直至信号或向导落盘后的延时退出
	}
	// 优雅关闭（SIGTERM/SIGINT 经上方 NotifyContext 触发，SPEC 4.8）：本地 API
	// 停收新请求 → 任务服务（取消在途任务 ctx → runner kill(-pgid) 整组终止
	// 子进程 → 在途任务以 needs_review/node_restarted_midway 落账收尾）→ 连接
	// （Run 返回即断）→ defer led.Close 释放 flock。子进程绝不成为孤儿跑完。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), closeBudget)
	defer cancel()
	_ = localHTTP.Shutdown(shutdownCtx)
	svc.Close()
	if client != nil {
		_ = client.Close()
	}
	if errors.Is(runErr, conn.ErrAuthFailed) {
		return fmt.Errorf("register 鉴权失败（auth_failed 不重试，请核对 node_id 与 register_token）: %w", runErr)
	}
	return runErr
}

// closeBudget 本地 API 优雅关闭预算（调度节拍，非协议常量）。
const closeBudget = 5 * time.Second

// buildExecutors 由 profiles 装配执行器与产物根表（方案 3.5 可复用函数）：
// 启动装配与热重载回调（ConfigOps.ReloadExecutors）同源，杜绝两处漂移。
// base_env 干净基线，HOME=ConfigHome（空则 defaultHome = data_dir/home）。
func buildExecutors(profiles []protocol.ExecProfile, baseEnv map[string]string, defaultHome string) (map[string]service.Runner, map[string]string) {
	runners := make(map[string]service.Runner, len(profiles))
	artifactRoots := make(map[string]string, len(profiles))
	for _, p := range profiles {
		runners[p.Name] = runner.New(p, baseEnv, defaultHome)
		artifactRoots[p.Name] = p.Workdir
	}
	return runners, artifactRoots
}

// detectSupervisor 保活检测（方案 3.5「应用并重启」应答注明的依据）：
//   - SWARM_SUPERVISED=1：部署包 launchd plist / systemd unit 显式声明（首选，
//     部署模板统一注入）；
//   - systemd 手写 unit：INVOCATION_ID 环境变量由 systemd 恒注入；
//   - 父进程为 PID 1：macOS launchd 服务的经典特征。
//
// 三者均未命中 = 裸前台，控制台「应用并重启」应答注明手动重启命令。
func detectSupervisor() bool {
	if os.Getenv("SWARM_SUPERVISED") == "1" || os.Getenv("INVOCATION_ID") != "" {
		return true
	}
	return os.Getppid() == 1
}

// newToken 生成 256bit 随机令牌的 hex 编码。
func newToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// ensureTokenFile 读取或生成 256bit 随机令牌并存 path；已有令牌文件则复用
//（重启不换令牌）。
func ensureTokenFile(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return string(b), nil
	}
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	if err := writeTokenFile(path, tok); err != nil {
		return "", err
	}
	return tok, nil
}

// writeTokenFile 原子写令牌文件：独占临时文件（os.CreateTemp 同目录唯一名——
// 固定 .tmp 名会被并发/残留复用，且已存在临时文件的权限不被 WriteFile 收紧，
// 返工 #5 同型修复）+ 显式 0600 + rename。
func writeTokenFile(path, tok string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后文件已不存在，Remove 报错无害
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(tok); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// loadConfig 读取并解析 yaml 配置：文件不存在原样上抛 os.ErrNotExist（三态判定
// 的「setup」信号）；其余读失败与解析失败一律包装报错（三态判定的「退出」信号）。
func loadConfig(path string) (*nodeConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("读取配置 %s 失败: %w", path, err)
	}
	var cfg nodeConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败: %w", path, err)
	}
	return &cfg, nil
}
