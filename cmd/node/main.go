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
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mtl802/swarmmind/internal/node/conn"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/node/localapi"
	"github.com/mtl802/swarmmind/internal/node/runner"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/node/sessionx"
	"github.com/mtl802/swarmmind/internal/notify"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// nodeConfig configs/node.yaml 结构（SPEC 五/六 + 方案 3.2.1 prime_agent）。
// agents 条目用 localapi.AgentProfile（yaml 镜像自 cmd/node 迁入共享——方案 3.5
// 配置编辑端点与装配方同源解析，protocol 包保持纯协议层）。
type nodeConfig struct {
	NodeID        string                  `yaml:"node_id"`
	HubURL        string                  `yaml:"hub_url"` // 形态开关：空 = standalone（方案 3.1）
	RegisterToken string                  `yaml:"register_token"`
	HubPubkeyFP   string                  `yaml:"hub_pubkey_fp"` // SPKI SHA-256；空则 TOFU 记录首连（Loop 4 已实现）
	LocalListen   string                  `yaml:"local_listen"`  // 本地 API 监听（默认 127.0.0.1:8445，强制回环）
	LocalToken    string                  `yaml:"local_token"`   // 本地 API 管理令牌（空则生成存 data_dir/local_token）
	PrimeAgent    string                  `yaml:"prime_agent"`   // 本机唯一总理声明（仅展示；prime_token 存 data_dir/prime_token，方案 3.2.1）
	Concurrency   int                     `yaml:"concurrency"`
	DataDir       string                  `yaml:"data_dir"`
	BaseEnv       map[string]string       `yaml:"base_env"`
	Agents        []localapi.AgentProfile `yaml:"agents"`
	// TerminalRetention 账本 result_acked 条目保留时长（maturity-retention-config）：
	// 字符串 duration（30m/24h/720h），经 protocol.ParseTerminalRetention 解析；
	// 空 = 默认 24h；0s（含 yaml 裸写数字 0，标量转换）= 永不自动清理；正值最小
	// 粒度 1 秒（亚秒拒绝启动）；保留期即幂等去重窗口（SPEC 4.5/4.6）。
	TerminalRetention string `yaml:"terminal_retention"`
}

// version 二进制版本（Makefile LDFLAGS -X main.version 注入 git describe；
// 未注入时为 dev）。--version 与本机 API status 端点 server_version 同源。
var version = "dev"

func main() {
	// service 子命令（P4② Windows 服务化：install/remove/start/stop/status；
	// 非 Windows 报错指引 systemd/launchd）
	if len(os.Args) > 1 && os.Args[1] == "service" {
		if err := serviceCommand(os.Args[2:]); err != nil {
			log.Fatalf("[node] %v", err)
		}
		return
	}
	cfgPath := flag.String("config", "configs/node.example.yaml", "配置文件路径（yaml；不存在则进 setup 模式）")
	dataDir := flag.String("data-dir", "", "覆盖配置中的 data_dir")
	showVersion := flag.Bool("version", false, "打印版本号后退出")
	flag.Parse()

	if *showVersion {
		fmt.Printf("swarmmind-node %s\n", version)
		return
	}

	// SCM 拉起（Windows 服务上下文）：进服务主循环，复用下方 run() 全路径；
	// 前台/非 Windows 恒返回 false，行为零变化。
	if handled, err := maybeRunService(*cfgPath, *dataDir); err != nil {
		log.Fatalf("[node] Windows 服务模式启动失败: %v", err)
	} else if handled {
		return
	}

	if err := run(*cfgPath, *dataDir); err != nil {
		log.Fatalf("[node] %v", err)
	}
}

// setupRestartDelay 向导落盘后延时优雅退出的预算：留出 HTTP 应答送达用户浏览器的
// 时间（方案 3.3「应答先返回 → 延时退出」），由 launchd/systemd KeepAlive 拉起。
const setupRestartDelay = 1500 * time.Millisecond

// svcStopChan Windows 服务模式停机通道：service_windows.go（仅 //go:build
// windows）在 svc.Handler.Execute 里设置并在 SCM Stop/Shutdown 时关闭，run()
// 内经 mergeStopChan 桥接为 ctx 取消——与 SIGTERM 同一条优雅关闭路径。前台
// 运行与非 Windows 平台恒 nil，行为零变化。
var svcStopChan chan struct{}

// svcReadyChan Windows 服务模式初始化就绪通道：run() 完成账本恢复、本地 API
// 监听与恢复任务重调度后关闭，服务主循环收到即报 Running（此前 SCM 一直看到
// StartPending，返工建议5）。前台/非 Windows 恒 nil，行为零变化。
var svcReadyChan chan struct{}

// mergeStopChan 在父 ctx 基础上并入外部停机通道：任一触发（通道关闭或父 ctx
// 取消）即取消返回的 ctx。ch 为 nil 原样返回父 ctx（前台/非 Windows 恒此路径）。
func mergeStopChan(parent context.Context, ch <-chan struct{}) context.Context {
	if ch == nil {
		return parent
	}
	ctx, cancel := context.WithCancel(parent)
	go func() {
		defer cancel() // 两分支退出都收口，不留悬挂 cancel
		select {
		case <-ch:
		case <-ctx.Done():
		}
	}()
	return ctx
}

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
	// 账本终态保留（maturity-retention-config）：非法值拒绝启动并给人话报错
	//（对齐 insecure_loopback 的严格风格，不静默回退掩盖笔误）；setup 模式
	// 无配置走默认（空串回落 24h），向导落盘后可配。
	retention, err := protocol.ParseTerminalRetention(cfg.TerminalRetention)
	if err != nil {
		return err
	}
	log.Printf("[node] 终态保留 terminal_retention=%s（默认 24h；0s=永不自动清理；保留期即幂等去重窗口）", retention)
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

	// Windows 兜底 Job（返工必修4③）：本进程整体加入 Kill-on-Job-Close Job，
	// 进程被杀/崩溃时 OS 兜底终止全部遗留子进程（任务级 Job 之外的双保险；显式
	// breakaway 按需豁免）。失败仅告警不拒启：任务级治理（spawn 侧致命拦截）
	// 不依赖本兜底，优雅关闭路径照常整组收尾；非 Windows 平台恒 nil 错误。
	if err := runner.InitSelfJobGuard(); err != nil {
		log.Printf("[node] 兜底 Job 加入失败（进程异常终止时遗留子进程可能残留）: %v", err)
	}

	// Windows 服务模式（svcStopChan 已设置）：无控制台，标准 log 落
	// data_dir/logs/node.log（16MiB 单代轮转，见 servicelog.go）。打开失败降级
	// stderr 继续跑（服务起停事件 SCM 事件日志仍可见），不因日志盘故障拒服。
	if svcStopChan != nil {
		if f, err := openServiceLog(cfg.DataDir); err != nil {
			log.Printf("[node] 服务日志文件打开失败（降级 stderr）: %v", err)
		} else {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
			defer f.Close()
			log.Printf("[node] ===== 服务进程启动（配置 %s，数据 %s）=====", cfgPath, cfg.DataDir)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Windows 服务模式并入 SCM Stop/Shutdown（svcStopChan 由 service_windows.go
	// 设置）；前台/非 Windows 恒 nil，mergeStopChan 原样返回，行为零变化。
	ctx = mergeStopChan(ctx, svcStopChan)

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

	// 会话内核装配（T2 持久会话，SPEC 六.五）：按 SessionAdapter 声明集构建
	// sessionx 装配表（环境沿 base_env 干净基线 + HOME=ConfigHome，SPEC 五同
	// runner 口径）。配置错误（未知适配器/相对路径命令/负超时）拒绝启动——宁可
	// 不起也不带病装配（sessionx 装配纪律）。无声明 agent = 空表，会话轮任务
	// 诚实失败 session_unsupported。热重载不换表（session_adapter 变更归
	// structural 类，须「应用并重启」，classifyProfilesChange）。
	sessionSpecs := buildSessionAgents(profiles, cfg.BaseEnv, defaultHome)
	sessions, err := sessionx.NewManager(cfg.DataDir, sessionSpecs, sessionx.Options{Log: log.Default()})
	if err != nil {
		return fmt.Errorf("装配会话内核失败: %w", err)
	}

	// 外连客户端仅组网态装配（方案 3.1：setup/standalone 不连 hub）。service
	// 三态都需要一个 transport：无 hub 端点的孤端点充当「本机黑洞汇」——出站帧
	// 一律 transport.ErrNoEndpoint（service 按良性静默），历史 hub 条目的迟到重发
	// 不出本机；本地任务本就被 origin 堵点拦在发送之前（方案 3.2）。
	// 通知装配（2026-10-26 伦哥裁决「同时通知」，取代 2026-10-24「networked 归
	// hub 不装配」旧口径）：三模式（standalone/networked/setup）都打开渠道存储
	// 并接 service 终态钩子——standalone 本机通知即唯一通知；networked 下 hub
	// 渠道照常，本机渠道是否跟发由 data_dir/notify_settings.json also_notify
	// 决定（默认 true，serviceNotifier 内 gate；false=本机静默 hub 一份照发）；
	// setup 无 agents 无任务不触达（装配无害）。渠道存 data_dir/notify_channels.json
	//（与 hub 同一 JSON 存储形态，控制台运行时可改）；inbox 为 hub 控制台专属，
	// node 以 nil inbox 装配（管理面准入拒绝 inbox 类型，见 localapi notify.go）。
	disp, err := notifyDispatcherFor(cfg.DataDir)
	if err != nil {
		return err
	}
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
	svcCfg := service.Config{
		Ledger:        led,
		Runners:       runners,
		ArtifactRoots: artifactRoots,
		Concurrency:   cfg.Concurrency,
		// spawn 回调（maturity-orphan-guard）：pgid + 组长启动指纹 + 启动周期
		// 即时落账本，崩溃重启后以「同周期 + pid + 指纹等值」精确识别孤儿实例
		//（R1 必修1/R2 必修1）；Windows 恒 (0,"")（Job 兑底）自然不落。落盘失败
		// 仅降级（恢复时放过清扫，安全侧）。
		OnSpawn: func(t protocol.Task, pgid, procStart int64, bootIdent string) {
			if err := led.MarkSpawned(t.ID, pgid, procStart, bootIdent); err != nil {
				log.Printf("[node] pgid 落盘失败（降级：恢复时放过清扫）task=%s pgid=%d: %v", t.ID, pgid, err)
			}
		},
		// 通知钩子（serviceNotifier 收口）：三模式均非 nil disp，包装差异在
		// 模式——networked 加「同时通知」gate（读 also_notify 决定本机渠道是否
		// 跟发），standalone 直连（本机通知即唯一通知）；nil dispatcher 仍须返回
		// 真 nil 接口（typed-nil 会使 Notifier==nil 判空失效，见 serviceNotifier）。
		Notifier: serviceNotifier(disp, mode, cfg.DataDir),
		// 账本终态保留（maturity-retention-config）：ParseTerminalRetention 解析值
		// 注入，service.cleanupLoop 周期清 result_acked 超期条目（SPEC 4.6/4.8）。
		TerminalRetention: &retention,
		// 会话内核（T2）：hub 下发会话轮任务（Task.Session 段）与本机控制台
		// 会话端点共用的执行内核；nil 时会话轮任务诚实失败。
		Sessions: sessions,
	}
	svc = service.New(svcCfg, tr)
	// 跨机取回（P4④第二子轮/SPEC 4.12）：仅 networked 启用（须有 hub 中继；
	// standalone/setup 语义不成立）。帧通道借 Service 自身收发，New 后置位即
	// 可，无装配序问题；未启用时组合解析器 Remote 位为空，与第一子轮行为一致。
	if mode == localapi.ModeNetworked {
		svc.EnableRemoteInputs(0, 0)
	}

	// 恢复终态通知（返工必修2）：启动恢复直接落账的终态（离线期间过期、started
	// 崩溃重启转 needs_review）不经 MarkFinished，运行期 notifyTerminal 旁路接
	// 不到，恢复清单里新降级条目在此补发。降级是 recover 单写事务、每任务至多
	// 一条且重启后已 finished 不再降级——同一任务只通知一次（去重锚点在账本恢复
	// 逻辑）。networked 侧同样经「同时通知」gate（Notifier 包装，见 serviceNotifier）。
	svc.NotifyRecoveredTerminal(rec.Downgraded)

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
		Version:    version, // 本机控制台页脚 server_version 数据源（status 端点下发）
		Setup:      mode == localapi.ModeSetup,
		Service:    svc,
		Agents:     agentState,
		Ledger:     led,
		TaskLog:    taskLog,
		LocalToken: localToken,
		PrimeToken: primeTok,
		PrimeAgent: cfg.PrimeAgent,
		// declare 钦定端点（方案 3.2.1 网页化）：写启动时 --config 原路径 +
		// 回填本机监听（应答 mcp_config 的 SWARM_NODE_URL 数据源）。
		DeclareConfigPath: cfgPath,
		LocalListen:       cfg.LocalListen,
		DataDir:           cfg.DataDir, // 影子 home 兜底 data_dir/home（primewire 与 runner HOME 同口径）
		RotatePrime:       rotatePrime,
		ArtifactFetch:     svc.FetchArtifactLocal,
		Notify:            disp, // 三模式全装配（2026-10-26 同时通知）；notifyReady 守卫降级仅剩防御意义
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
	// 服务模式（返工建议5）：初始化全部就绪，svc 主循环此刻才向 SCM 报 Running。
	if svcReadyChan != nil {
		close(svcReadyChan)
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
	waitNotifyIdle(disp, closeBudget) // 在途通知投递收尾（预算内尽力等待，不拖关闭）
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

// serviceNotifier service 终态通知钩子装配（返工必修1收口 + 2026-10-26 同时通
// 知 gate）：nil dispatcher 必须返回真 nil 接口——nil 具体指针直接赋给接口会成
// 非 nil 的 typed-nil（Notifier==nil 判空失效），故经本函数收口（可测，cmd/node
// notify_test 锁定 nil⇒真 nil 语义）。模式差异：
//   - networked：包 alsoNotifyGate——每次终态读 data_dir/notify_settings.json
//     also_notify（默认 true=跟发；false=本机静默，hub 渠道一份照发）。读盘
//     不缓存：开关运行时即时生效，盘是唯一真相（notify.SettingsFile 同口径）。
//   - standalone：直连 dispatcher（本机通知即唯一通知，开关不参与门控）。
//   - setup：直连 dispatcher（无 agents 无任务不触达；启动恢复降级条目如存在
//     照常补发，渠道未配置自然静默）。
func serviceNotifier(d *notify.Dispatcher, mode string, dataDir string) service.TerminalNotifier {
	if d == nil {
		return nil
	}
	if mode == localapi.ModeNetworked {
		return alsoNotifyGate{disp: d, dataDir: dataDir}
	}
	return d
}

// alsoNotifyGate networked 模式的「同时通知」闸（service.TerminalNotifier 实现，
// 见 serviceNotifier）：gate 的是 node 本机渠道这份「跟发」，hub 渠道通知不经过
// node 侧（hub core 终态旁路），天然不受影响。
type alsoNotifyGate struct {
	disp    *notify.Dispatcher
	dataDir string
}

func (g alsoNotifyGate) TaskFinal(t protocol.Task) {
	if !notify.AlsoNotify(g.dataDir) {
		return
	}
	g.disp.TaskFinal(t)
}

// notifyDispatcherFor 通知装配套件（2026-10-26 起三模式统一装配，旧 standalone
// 门控随「同时通知」裁决退役）：打开 data_dir 渠道存储构造分发器。两个消费点——
// service Notifier 经 serviceNotifier（接口字段，必须拦 typed-nil）与 localapi
// Notify（具体指针字段，nil 指针赋值安全、notifyReady 判空即 503 降级）。收口
// 一处、可测（cmd/node notify_test）。存储打开失败按启动失败处理（与账本同口径：
// data_dir 不可写越早暴露越好）。
func notifyDispatcherFor(dataDir string) (*notify.Dispatcher, error) {
	return openNotifyDispatcher(dataDir)
}

// openNotifyDispatcher 通知装配：打开 data_dir 渠道存储（notify_channels.json，
// 与 hub 同一 JSON 存储形态），构造分发器（inbox=nil——站内通知 hub 控制台专属）。
func openNotifyDispatcher(dataDir string) (*notify.Dispatcher, error) {
	channels, err := notify.OpenChannelStore(dataDir)
	if err != nil {
		return nil, fmt.Errorf("打开通知渠道存储失败: %w", err)
	}
	return notify.NewDispatcher(channels, nil), nil
}

// waitNotifyIdle 通知在途投递收尾（对齐 cmd/hub 同名函数）：预算内尽力等待
// （通知是出站 HTTP，独立于监听器生命周期）；超时放弃——通知本身非可靠投递
// 机制（崩溃在发出前即漏发），关闭不被它拖住。disp 为 nil（旧构造方）零开销。
func waitNotifyIdle(disp *notify.Dispatcher, budget time.Duration) {
	if disp == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		disp.WaitIdle()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(budget):
		log.Printf("[node] 通知在途投递等待超时（%s），放弃等待（不影响关闭）", budget)
	}
}

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

// buildSessionAgents 由 profiles 装配会话内核配置表（T2）：仅取声明了
// session_adapter 的能力；Command/PermissionMode/TimeoutSec 沿 ExecProfile 同源
// 语义（权限空值由 sessionx 取适配器保守默认，绝不 yolo）；Env = base_env 展开
// + HOME=ConfigHome（空则 defaultHome），与 runner 进程环境同口径（SPEC 五）。
func buildSessionAgents(profiles []protocol.ExecProfile, baseEnv map[string]string, defaultHome string) map[string]sessionx.AgentSpec {
	specs := make(map[string]sessionx.AgentSpec)
	for _, p := range profiles {
		if p.SessionAdapter == "" {
			continue
		}
		home := p.ConfigHome
		if home == "" {
			home = defaultHome
		}
		env := make([]string, 0, len(baseEnv)+1)
		for k, v := range baseEnv {
			if k == "HOME" {
				continue // HOME 固定 ConfigHome 口径，同 runner（SPEC 五）
			}
			env = append(env, k+"="+v)
		}
		sort.Strings(env) // 确定序（map 遍历无序，序列化与日志可复现）
		env = append(env, "HOME="+home)
		specs[p.Name] = sessionx.AgentSpec{
			Adapter:        p.SessionAdapter,
			Command:        p.Command,
			PermissionMode: p.PermissionMode,
			TimeoutSec:     p.TimeoutSec,
			Env:            env,
		}
	}
	return specs
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
// （重启不换令牌）。
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
