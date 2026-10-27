// hub 进程入口（SPEC 二）：node 会话监听（/ws/node，ws 或自签 TLS）+ 会话层 +
// hub 任务服务 + HTTP 管理面（api_listen，Loop 4）+ 优雅关闭。
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
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

	"github.com/mtl802/swarmmind/internal/decision"
	"github.com/mtl802/swarmmind/internal/hub/api"
	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/group"
	"github.com/mtl802/swarmmind/internal/notify"
	"github.com/mtl802/swarmmind/internal/hub/session"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// hubConfig configs/hub.yaml 结构（SPEC 五 hub 配置）。
type hubConfig struct {
	NodeListen string `yaml:"node_listen"` // node 会话入口（默认 :8443）
	APIListen  string `yaml:"api_listen"`  // 管理面监听（默认 127.0.0.1:8444，强制回环）
	TLS        struct {
		InsecureLoopback bool `yaml:"insecure_loopback"` // true=明文 ws + 强制回环监听
	} `yaml:"tls"`
	Nodes      map[string]string `yaml:"nodes"`       // node_id → register_token
	AdminToken string            `yaml:"admin_token"` // 管理面登录令牌（空则生成存 data_dir/admin_token）
	DataDir    string            `yaml:"data_dir"`    // 默认 data
	Notify     struct {
		WebhookURL string `yaml:"webhook_url"` // 旧版任务终态通知 webhook（兼容入口：启动时一次性导入为通知渠道）
		Format     string `yaml:"format"`      // raw（默认）| feishu（导入时映射渠道类型）
	} `yaml:"notify"` // 任务终态 IM 通知旧配置段（P4③ 控制台化后仅作一次性导入入口；日常配置改用管理台「设置 → 通知」）
	Decision struct {
		Provider   string `yaml:"provider"`    // llm-as-jev（P5.1 D1 唯一启用值）；留空/off 不启用
		Model      string `yaml:"model"`       // 决策模型（缺省 glm-5.3-flash）
		BaseURL    string `yaml:"base_url"`    // OpenAI 兼容端点（缺省 GLM coding plan）
		APIKey     string `yaml:"api_key"`     // 一级密钥；留空回落 GLM_API_KEY 环境变量，再回落 swarm-prime-home
		PrimeHome  string `yaml:"prime_home"`  // swarm-prime-home 路径；空回落 SWARM_PRIME_HOME，再回落 $HOME/swarm-prime-home
		TimeoutSec int    `yaml:"timeout_sec"` // 单次建议预算秒数（缺省 15，GLM 尾延迟实测口径）
	} `yaml:"decision"` // 派发建议决策服务（P5.1 D1，RFC-jev-dispatch）：决策是建议非必经，未启用/失败均不阻塞派单
}

// version 二进制版本（Makefile LDFLAGS -X main.version 注入 git describe；
// 未注入时为 dev）。--version、启动日志、管理台页脚（管理面 server_version
// 字段）三处同源。
var version = "dev"

func main() {
	cfgPath := flag.String("config", "configs/hub.example.yaml", "配置文件路径（yaml）")
	dataDir := flag.String("data-dir", "", "覆盖配置中的 data_dir")
	listen := flag.String("listen", "", "覆盖配置中的 node_listen")
	showVersion := flag.Bool("version", false, "打印版本号后退出")
	flag.Parse()

	if *showVersion {
		fmt.Printf("hub %s\n", version)
		return
	}
	if err := run(*cfgPath, *dataDir, *listen); err != nil {
		log.Fatalf("[hub] %v", err)
	}
}

func run(cfgPath, dataDirOverride, listenOverride string) error {
	log.Printf("[hub] 版本 %s", version)
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	if dataDirOverride != "" {
		cfg.DataDir = dataDirOverride
	}
	if listenOverride != "" {
		cfg.NodeListen = listenOverride
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.NodeListen == "" {
		cfg.NodeListen = ":8443"
	}
	if cfg.APIListen == "" {
		cfg.APIListen = "127.0.0.1:8444"
	}

	// 管理面（api_listen）：Loop 5 已实现登录制（admin_token），绑定地址放开为可配置；
	// 非回环绑定时打印明文 HTTP 警告（P3 收尾项：反代 TLS）。完全无 token 的配置仍
	// 拒绝公网绑定（无凭据即裸奔，保留 C4 防线）。
	if !isLoopbackListen(cfg.APIListen) {
		if cfg.AdminToken == "" {
			return fmt.Errorf("管理面绑定非回环地址 %s 但未配置 admin_token：公网管理面必须启用登录制", cfg.APIListen)
		}
		log.Printf("[hub] 警告：管理面绑定非回环地址 %s（HTTP 明文，令牌传输未加密；建议尽快前置 TLS 反代）", cfg.APIListen)
	}
	// insecure_loopback=true：明文 ws，且 node 入口监听地址强制回环（代码强制，
	// 非文档约束，SPEC 二；node 明文入口允许规格列出的 127.0.0.1/::1）。
	if cfg.TLS.InsecureLoopback {
		if err := session.ValidateLoopback(cfg.NodeListen); err != nil {
			return err
		}
		log.Printf("[hub] insecure_loopback=true：明文 ws 模式（仅回环）")
	} else {
		log.Printf("[hub] TLS 模式（自签证书存 %s）", cfg.DataDir)
	}
	log.Printf("[hub] HTTP 管理面监听 %s（登录制，admin_token）", cfg.APIListen)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("创建 data_dir 失败: %w", err)
	}
	// 管理令牌：配置为空则生成随机令牌落盘 data_dir/admin_token（0600），日志只报位置
	adminToken := cfg.AdminToken
	if adminToken == "" {
		tok, err := ensureGeneratedToken(filepath.Join(cfg.DataDir, "admin_token"))
		if err != nil {
			return fmt.Errorf("生成 admin_token 失败: %w", err)
		}
		adminToken = tok
		log.Printf("[hub] 未配置 admin_token：已生成随机令牌并存 %s（0600）", filepath.Join(cfg.DataDir, "admin_token"))
	}

	// 持久化层：data_dir flock（单实例）+ 先写后见事务（SPEC 4.7）
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("打开 hub store 失败: %w", err)
	}
	defer st.Close()

	// 群聊存储（GROUP-RFC 六 MVP）：独立快照 groups.json，先写后见同模式
	groups, err := group.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("打开群聊 store 失败: %w", err)
	}
	defer groups.Close()

	// 会话层与任务服务接线：Manager 实现 transport.Transport（membus 替换点，
	// SPEC 八.3）；SetOnline 经 late-bound 闭包接 core（core 构造需要 Manager）。
	var coreSvc *core.Core
	mgr := session.NewManager(session.Config{
		Store:  st,
		Tokens: cfg.Nodes,
		SetOnline: func(nodeID string, online bool) {
			if coreSvc != nil {
				coreSvc.SetOnline(nodeID, online)
			}
		},
	})
	// 任务终态通知（P4③ 控制台化升级）：Dispatcher 路由到通知渠道
	// （data_dir/notify_channels.json，管理台「设置 → 通知」运行时可改）。
	// hub.yaml 旧 notify 段兼容：配置了 webhook_url 时启动一次性导入（幂等，
	// 只导一次；此后该段不再生效，日常配置走管理台）。格式启动即校验——拼错
	// 尽快失败，不静默回落。
	channels, err := notify.OpenChannelStore(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("打开通知渠道存储失败: %w", err)
	}
	if cfg.Notify.WebhookURL != "" {
		format := cfg.Notify.Format
		switch format {
		case "", notify.FormatRaw, notify.FormatFeishu:
		default:
			return fmt.Errorf("notify 配置非法: 未知格式 %q（可选 raw|feishu）", format)
		}
		if _, err := channels.ImportLegacy(cfg.Notify.WebhookURL, format, time.Now()); err != nil {
			return fmt.Errorf("导入 hub.yaml notify 段失败: %w", err)
		}
	}
	inbox := &notify.Inbox{}
	notifier := notify.NewDispatcher(channels, inbox)
	coreSvc, err = core.New(core.Config{
		Store:     st,
		Transport: mgr,
		Notifier:  notifier,
		// 群聊闭环（GROUP-RFC）：任务终态自动回流引用它的群（幂等由群存储的
		// 已回流集合保证）；失败只记日志，不影响任务状态机。
		OnTaskTerminal: func(t protocol.Task) {
			if _, err := groups.RefluxTaskResult(t.ID, group.TaskTerminalBody(t)); err != nil {
				log.Printf("[hub] 任务终态回流群失败 task=%s: %v", t.ID, err)
			}
		},
		SweepInterval: time.Second,
	})
	if err != nil {
		return fmt.Errorf("构造 hub core 失败: %w", err)
	}
	defer coreSvc.Close()
	mgr.Subscribe(coreSvc.HandleFrame)

	// 群存储周期维护（2026-10-22 修复轮2）：启动即扫一次（恢复重启前的待补偿
	// 回流队列），此后定时清理 24h 过期消息 + 重试补偿队列（GROUP-RFC 三 独立
	// 轻量信箱约定兑现）。随优雅关闭先行停止（defer LIFO：先于 groups.Close）。
	groupSweepStop := make(chan struct{})
	defer close(groupSweepStop)
	go groupSweepLoop(groups, groupSweepStop)

	// node 会话监听（SPEC 二）
	mux := http.NewServeMux()
	mux.Handle("GET /ws/node", mgr)
	srv := &http.Server{Addr: cfg.NodeListen, Handler: mux}
	if !cfg.TLS.InsecureLoopback {
		cert, err := session.LoadCertificate(cfg.DataDir) // 首启自签生成（ECDSA P-256）
		if err != nil {
			return err
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	// HTTP 管理面（SPEC 六：五端点 + 登录鉴权，Loop 4；Groups 群聊 MVP 端点；
	// Enroll 部署包编译：源码根 SWARM_SOURCE_DIR（默认 /opt/swarmmind-src），
	// 编译缓存挂 data_dir/enroll-cache；NodeEntry 节点接入入口——接入向导 hub_url
	// 预填真相源，第六轮修复 2：管理台端口与节点入口端口不同时前端推导必错）
	nodeScheme := "wss"
	if cfg.TLS.InsecureLoopback {
		nodeScheme = "ws"
	}
	// 派发建议决策服务（P5.1 D1）：key 三级解析（配置→环境→prime-home），未启用
	// 返回不可用实例——端点形态稳定（恒 200 降级），派单路径不依赖它。
	homeDir, _ := os.UserHomeDir()
	decisionSvc := decision.New(decision.Settings{
		Provider: cfg.Decision.Provider, Model: cfg.Decision.Model, BaseURL: cfg.Decision.BaseURL,
		APIKey: cfg.Decision.APIKey, PrimeHome: cfg.Decision.PrimeHome, TimeoutSec: cfg.Decision.TimeoutSec,
	}, os.Getenv, homeDir, log.Printf)
	apiSrv := &http.Server{Addr: cfg.APIListen, Handler: api.New(api.Config{
		Core: coreSvc, AdminToken: adminToken, DataDir: cfg.DataDir, Groups: groups,
		Notify:    notifier,
		Decision:  decisionSvc,
		Version:   version, // 管理台页脚 server_version 数据源（登录响应/登录态账户视图下发）
		NodeEntry: &api.NodeEntryConfig{Scheme: nodeScheme, Listen: cfg.NodeListen},
		Enroll: &api.EnrollConfig{
			SourceDir: envDefault("SWARM_SOURCE_DIR", "/opt/swarmmind-src"),
			CacheDir:  filepath.Join(cfg.DataDir, "enroll-cache"),
		},
	}).Handler()}
	apiLn, err := net.Listen("tcp", cfg.APIListen)
	if err != nil {
		return fmt.Errorf("管理面监听 %s 失败: %w", cfg.APIListen, err)
	}
	go func() {
		if err := apiSrv.Serve(apiLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[hub] 管理面监听异常退出: %v", err)
		}
	}()
	log.Printf("[hub] HTTP 管理面已启动 %s（/api/v1/*）", cfg.APIListen)

	ln, err := net.Listen("tcp", cfg.NodeListen)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", cfg.NodeListen, err)
	}
	serveErr := make(chan error, 1)
	go func() {
		if cfg.TLS.InsecureLoopback {
			serveErr <- srv.Serve(ln)
		} else {
			serveErr <- srv.ServeTLS(ln, "", "") // 证书经 TLSConfig 注入
		}
	}()
	log.Printf("[hub] node 会话入口已启动 %s（path=/ws/node）", cfg.NodeListen)

	// 优雅关闭（SPEC 九：http.Server.Shutdown 不管 WebSocket——先遍历会话发
	// Close 帧再等退出；顺序：管理面停收新请求 → 会话关闭 → node 入口停收 →
	// core/store 由 defer 收尾）
	const closeBudget = 10 * time.Second
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Printf("[hub] 收到信号 %s，开始优雅关闭", s)
	case err := <-serveErr:
		// L3：监听异常返回路径同样先走会话清理，再关闭 core/store（defer 序）
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			shutdownHub(apiSrv, mgr, srv, closeBudget, notifier)
			return fmt.Errorf("hub 监听异常退出: %w", err)
		}
		shutdownHub(apiSrv, mgr, srv, closeBudget, notifier)
		return nil
	}
	shutdownHub(apiSrv, mgr, srv, closeBudget, notifier)
	return nil
}

// shutdownHub 统一关闭顺序：通知在途投递收尾（预算内尽力等待）→ 管理面 →
// 会话（Close 帧+预算等待）→ node 入口。
func shutdownHub(apiSrv *http.Server, mgr *session.Manager, srv *http.Server, budget time.Duration, disp *notify.Dispatcher) {
	waitNotifyIdle(disp, budget)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	_ = apiSrv.Shutdown(ctx) // 管理面先停（不再受理新提交）
	if err := mgr.CloseWithBudget(budget); err != nil {
		log.Printf("[hub] 会话关闭异常: %v", err)
	}
	_ = srv.Shutdown(ctx)
}

// waitNotifyIdle 通知在途投递收尾：预算内尽力等待（通知是出站 HTTP，独立于
// 监听器生命周期）；超时放弃——通知本身非可靠投递机制（崩溃在发出前即漏发），
// 关闭不被它拖住。
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
		log.Printf("[hub] 通知在途投递等待超时（%s），放弃等待（不影响关闭）", budget)
	}
}

// groupSweepInterval 群存储维护周期：24h 过期清理与补偿回流重试均非时敏，
// 5 分钟一扫足够（回流失败入队见 OnTaskTerminal → RefluxTaskResult 失败路径）。
const groupSweepInterval = 5 * time.Minute

// groupSweepLoop 群存储维护循环：启动即扫一次（恢复待补偿回流队列），此后
// 按周期执行，直至 stop 关闭（优雅关闭时由 defer 触发）。
func groupSweepLoop(groups *group.Store, stop <-chan struct{}) {
	if n := groups.Sweep(time.Now().UTC()); n > 0 {
		log.Printf("[hub] 群存储启动清理：过期消息 %d 条", n)
	}
	tk := time.NewTicker(groupSweepInterval)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			if n := groups.Sweep(time.Now().UTC()); n > 0 {
				log.Printf("[hub] 群存储周期清理：过期消息 %d 条", n)
			}
		}
	}
}

// envDefault 环境变量读取（空值回落默认）。
func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ensureGeneratedToken 生成 256bit 随机令牌；已有令牌文件则复用（重启不换令牌，
// 否则调用方无法登录）。
func ensureGeneratedToken(path string) (string, error) {
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
func loadConfig(path string) (*hubConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	var cfg hubConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败: %w", path, err)
	}
	return &cfg, nil
}

// isLoopbackListen 判定监听地址是否为回环（host 部分为 127.0.0.1、::1 或 localhost）。
func isLoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}
