//go:build windows

// Windows 服务化（PRODUCT-DIRECTION-v2 P4②）：swarmmind-node 自身即服务二进制
// ——install 把「本 exe + --config 绝对路径」注册进 SCM（服务名 swarmmind-node，
// AUTOMATIC 开机自启）；SCM 拉起本进程时 svc.IsWindowsService 命中 → svc.Run
// 进服务主循环，run() 既有启动/优雅关闭全路径原样复用（三态判定、账本恢复、
// 本地 API、svc.Close 收尾），绝不绕行。
//
// 子命令放 node 而非 swarmmindctl 的理由：服务本体是 node 进程，注册路径与
// 执行路径同二进制永不错位；swarmmindctl 是 hub 管理面薄包装（SPEC 六），不掺
// node 本机运维。
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName        = "swarmmind-node"
	serviceDisplayName = "SwarmMind Node"
	serviceDescription = "SwarmMind 节点：接收 hub 派单并在本机执行 agent，开机自启。停止服务会一并终止执行中的任务进程。"
)

// 故障恢复（P4 Gate 无人值守稳定）：进程自退（向导落盘/配置应用重启）、崩溃与
// 非崩溃失败（配置/运行错误以非零退出码收场）均触发 SCM 恢复动作，按下表自动
// 重启（超出动作表沿用末项，最坏 1 次/分钟，配合日志可排查）；24h 无失败重置
// 计数。非崩溃失败经 SetRecoveryActionsOnNonCrashFailures 显式启用（SCM 默认只
// 对进程崩溃触发，返工必修3）。SCM 主动 stop/shutdown 走干净退出（退出码 0），
// 不触发恢复。
var serviceRecovery = []mgr.RecoveryAction{
	{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
}

const serviceRecoveryResetPeriod = 24 * 3600 // 秒：失败计数重置周期

// serviceCommand service 子命令入口（install/remove/start/stop/status）。
func serviceCommand(args []string) error {
	opts, err := parseServiceArgs(args)
	if err != nil {
		return err
	}
	switch opts.action {
	case svcActInstall:
		return serviceInstall(opts)
	case svcActRemove:
		return serviceRemove()
	case svcActStart:
		return serviceStart()
	case svcActStop:
		return serviceStop()
	default:
		return serviceStatus()
	}
}

// connectMgr 连接 SCM；权限不足给人话指引（mgr.Connect 需管理员）。
func connectMgr() (*mgr.Mgr, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("连接服务管理器失败（service 子命令需管理员权限：右键「以管理员身份运行」PowerShell）: %w", err)
	}
	return m, nil
}

// openInstalled 打开本服务；未安装时给人话提示。
func openInstalled(m *mgr.Mgr) (*mgr.Service, error) {
	s, err := m.OpenService(serviceName)
	if err != nil {
		return nil, fmt.Errorf("服务 %s 未安装（先执行 service install）", serviceName)
	}
	return s, nil
}

// serviceInstall 注册服务：BinaryPathName = 本 exe（绝对路径）+ --config（绝对
// 路径）[+ --data-dir]，AUTOMATIC 开机自启，故障自动重启见 serviceRecovery。
func serviceInstall(opts *serviceOpts) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return fmt.Errorf("解析自身可执行文件绝对路径失败: %w", err)
	}
	cfgAbs, err := filepath.Abs(opts.config)
	if err != nil {
		return fmt.Errorf("解析配置绝对路径失败: %w", err)
	}
	// 防呆：示例配置注册成服务 = 开机拉起一个必进 setup 的空节点
	if strings.HasSuffix(strings.ToLower(cfgAbs), "node.example.yaml") {
		return fmt.Errorf("拒绝把示例配置注册为服务：%s（请提供真实 node.yaml）", cfgAbs)
	}
	m, err := connectMgr()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return fmt.Errorf("服务 %s 已存在；如需重装先执行: %s service remove", serviceName, exe)
	}
	svcArgs := []string{"--config", cfgAbs}
	if opts.dataDir != "" {
		// 服务工作目录不保证（默认 System32），--data-dir 一律绝对化后再注册
		//（返工建议5），服务环境相对路径不漂移。
		dataAbs, err := filepath.Abs(opts.dataDir)
		if err != nil {
			return fmt.Errorf("解析 data-dir 绝对路径失败: %w", err)
		}
		svcArgs = append(svcArgs, "--data-dir", dataAbs)
	}
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		DisplayName:  serviceDisplayName,
		Description:  serviceDescription,
	}, svcArgs...)
	if err != nil {
		return fmt.Errorf("创建服务失败（需管理员权限）: %w", err)
	}
	defer s.Close()
	if err := s.SetRecoveryActions(serviceRecovery, serviceRecoveryResetPeriod); err != nil {
		return fmt.Errorf("服务已创建，但配置故障自动重启失败（不影响手动使用）: %w", err)
	}
	// 非崩溃失败（非零退出码收场：配置错误/运行错误/进程自退）同样触发恢复动作：
	// SCM 默认只对进程崩溃触发，不开此开关则「启动后立即停止」类失败无人接管
	//（返工必修3，与 serviceRecovery 配套）。
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("服务已创建，但启用非崩溃失败自动重启失败（不影响手动使用）: %w", err)
	}
	fmt.Printf("服务已注册：%s（开机自启，故障自动重启 5s/30s/60s 退避，非崩溃失败同样触发）\n  二进制: %s\n  配置: %s\n下一步: 管理员运行 %s service start，或重启机器自动拉起\n",
		serviceName, exe, cfgAbs, filepath.Base(exe))
	return nil
}

// serviceRemove 卸载服务。运行中拒绝：停止会终止执行中的任务，须显式先 stop。
func serviceRemove() error {
	m, err := connectMgr()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := openInstalled(m)
	if err != nil {
		return err
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		return fmt.Errorf("服务正在运行（%s），请先执行 service stop 再卸载（停止会终止执行中的任务）", stateHuman(st.State))
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("删除服务失败: %w", err)
	}
	fmt.Printf("服务已卸载：%s（下次开机不再拉起）\n", serviceName)
	return nil
}

// serviceStart 启动服务并轮询至 Running；启动后立即回 Stopped 多为配置问题，
// 指引先前台运行核对。
func serviceStart() error {
	m, err := connectMgr()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := openInstalled(m)
	if err != nil {
		return err
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State == svc.Running {
		fmt.Printf("服务已在运行（PID %d）\n", st.ProcessId)
		return nil
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("启动服务失败: %w（详情见 data_dir/logs/node.log）", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		st, err := s.Query()
		if err == nil {
			switch st.State {
			case svc.Running:
				fmt.Printf("已启动：%s（PID %d）；日志见 data_dir/logs/node.log\n", serviceName, st.ProcessId)
				return nil
			case svc.Stopped:
				return errors.New("服务启动后立即停止——常见原因：配置缺失/非法或端口被占（服务管理器会按 5s/30s/60s 退避自动重启，配置错误将持续重启失败）；请先前台运行 node --config <路径> 核对（详见 data_dir/logs/node.log）")
			}
		}
		if time.Now().After(deadline) {
			state := "未知"
			if err == nil {
				state = stateHuman(st.State)
			}
			return fmt.Errorf("等待服务进入运行态超时（当前 %s）", state)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// serviceStop 发送 SCM Stop 并轮询至 Stopped：优雅关闭预算 = 任务取消收尾
// （svc.Close）+ 本地 API 摘除，20s 超时如实报告（不强杀，交人工排查）。
func serviceStop() error {
	m, err := connectMgr()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := openInstalled(m)
	if err != nil {
		return err
	}
	defer s.Close()
	st, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("发送停止指令失败（服务可能未运行）: %w", err)
	}
	if st.State == svc.Stopped {
		fmt.Println("服务未在运行")
		return nil
	}
	fmt.Println("停止中（执行中的任务按服务停止语义整组终止收尾）……")
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, err := s.Query()
		if err == nil && st.State == svc.Stopped {
			fmt.Printf("已停止：%s\n", serviceName)
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("等待服务停止超时（20s）——进程仍在优雅收尾，稍后用 service status 确认；持续不退请查 data_dir/logs/node.log")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// serviceStatus 人话展示服务状态（状态/启动类型/PID/启动命令）。
func serviceStatus() error {
	m, err := connectMgr()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := openInstalled(m)
	if err != nil {
		return err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("查询服务状态失败: %w", err)
	}
	cfg, err := s.Config()
	if err != nil {
		return fmt.Errorf("读取服务配置失败: %w", err)
	}
	fmt.Printf("服务: %s（%s）\n状态: %s\n启动类型: %s\n", serviceName, cfg.DisplayName,
		stateHuman(st.State), startTypeHuman(cfg.StartType))
	if st.ProcessId != 0 {
		fmt.Printf("PID: %d\n", st.ProcessId)
	}
	if cfg.BinaryPathName != "" {
		fmt.Printf("启动命令: %s\n", cfg.BinaryPathName)
	}
	switch st.State {
	case svc.Running:
		fmt.Println("下一步: 无需操作；日志见 data_dir/logs/node.log")
	case svc.Stopped:
		fmt.Println("下一步: service start 启动（需管理员）")
	default:
		fmt.Println("下一步: 稍后 service status 复查")
	}
	return nil
}

// stateHuman 服务状态人话（INTERACTION 黑话清扫同口径）。
func stateHuman(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "已停止"
	case svc.StartPending:
		return "启动中"
	case svc.StopPending:
		return "停止中"
	case svc.Running:
		return "运行中"
	case svc.Paused:
		return "已暂停"
	}
	return fmt.Sprintf("未知(%d)", uint32(s))
}

// startTypeHuman 启动类型人话（其余类型极少用，原文展示）。
func startTypeHuman(t uint32) string {
	switch t {
	case mgr.StartAutomatic:
		return "自动（开机自启）"
	case mgr.StartManual:
		return "手动"
	case mgr.StartDisabled:
		return "已禁用"
	}
	return fmt.Sprintf("其他(%d)", t)
}

// maybeRunService SCM 拉起检测：命中服务上下文则进服务主循环（阻塞至服务停
// 止），返回 true；前台运行返回 false 照常走命令行路径。
func maybeRunService(cfgPath, dataDir string) (bool, error) {
	is, err := svc.IsWindowsService()
	if err != nil {
		return false, fmt.Errorf("检测 Windows 服务上下文失败: %w", err)
	}
	if !is {
		return false, nil
	}
	return true, svc.Run(serviceName, &nodeService{cfgPath: cfgPath, dataDir: dataDir})
}

// nodeService svc.Handler：服务主循环。node 主逻辑复用 run() 全路径；SCM
// Stop/Shutdown 经关闭 svcStopChan 桥接为 run() 内 ctx 取消——与 SIGTERM 同
// 一条优雅关闭路径（任务整组终止收尾、账本落账、本地 API 摘除）。
type nodeService struct {
	cfgPath string
	dataDir string
}

func (s *nodeService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending, WaitHint: 5000}
	// 服务默认工作目录是 System32：切到 exe 目录，相对路径（data/、configs/）不
	// 漂移；切不动如实记录（install 侧 --config/--data-dir 已绝对化，残余风险
	// 仅配置内手写相对路径，日志给指向）。
	if exe, err := os.Executable(); err != nil {
		log.Printf("[node] 服务定位自身可执行文件失败（工作目录保持系统默认）: %v", err)
	} else if err := os.Chdir(filepath.Dir(exe)); err != nil {
		log.Printf("[node] 服务切换工作目录到 %s 失败，相对路径配置将落在系统目录（建议 install 全部用绝对路径）: %v", filepath.Dir(exe), err)
	}
	// 服务环境即保活环境（方案 3.5「应用并重启」应答按此识别，复用既有检测）
	os.Setenv("SWARM_SUPERVISED", "1")
	svcStopChan = make(chan struct{})
	svcReadyChan = make(chan struct{})
	runErr := make(chan error, 1)
	go func() { runErr <- run(s.cfgPath, s.dataDir) }()

	// Running 上报时机（返工建议5）：等 run() 初始化完成（账本/恢复重调度/本地
	// API 就绪）经 svcReadyChan 通知后才报 Running——此前 SCM 一直看到
	// StartPending，service start 不再被「假运行」误导。
	//
	// StartPending 续报（返工必修1）：启动上报只发一次（WaitHint=5s），账本恢复
	// 等初始化超 5s 时 SCM 判启动超时。Ready 前每 3s 续报 StartPending 且
	// CheckPoint 递增（SCM 收到即刷新等待预算，CheckPoint 递增表示有进展），
	// 初始化再慢不误杀；Stop 后不再回 StartPending（状态不得回退，Stop 预算
	// 30s 覆盖优雅关闭设计上限）。
	stopping := false
	ready := false
	checkpoint := uint32(0)
	pendingTick := time.NewTicker(3 * time.Second)
	defer pendingTick.Stop()
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				if stopping {
					continue // 重复 Stop 只处理一次
				}
				stopping = true
				status <- svc.Status{State: svc.StopPending, WaitHint: 30000}
				close(svcStopChan) // 桥接优雅关闭（与 SIGTERM 同路径）
			}
		case <-svcReadyChan:
			svcReadyChan = nil // 通道已关闭，置 nil 防重复触发
			ready = true
			if !stopping {
				status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
			}
		case <-pendingTick.C:
			if ready || stopping {
				continue // Running 后无 pending 预算可续；StopPending 预算独立计时
			}
			checkpoint++
			status <- svc.Status{State: svc.StartPending, WaitHint: 5000, CheckPoint: checkpoint}
		case err := <-runErr:
			return executeTerminal(err, stopping)
		}
	}
}

// executeTerminal 终态返回口径（返工必修2）：退出码统一经 Execute 返回值交
// x/sys svc 合成唯一的 SERVICE_STOPPED 终态（Win32ExitCode/ServiceSpecificExit-
// Code），handler 不再手工发 Stopped 状态——旧实现先发无错误码的 Stopped，SCM
// 可能以「成功停止」收场，非崩溃恢复无从触发。干净停止（SCM Stop/Shutdown 且
// run 无错）返回 (false, 0) 不触发恢复；其余（配置/运行错误、向导落盘与「应用
// 并重启」自退）返回 (true, 1) 服务专属非零码，配合 SetRecoveryActionsOnNonCrash
// Failures 交故障恢复自动重启——对齐 launchd/systemd KeepAlive 语义（方案 3.3）。
func executeTerminal(err error, stopping bool) (bool, uint32) {
	switch {
	case err != nil:
		if stopping {
			log.Printf("[node] 服务停止过程中出错: %v", err)
		} else {
			log.Printf("[node] 服务进程异常退出: %v（交由服务管理器自动重启）", err)
		}
		return true, 1
	case stopping:
		return false, 0 // 干净停止：不触发故障恢复重启
	default:
		log.Printf("[node] 服务进程自行退出（配置切换等），交由服务管理器自动重启")
		return true, 1
	}
}
