// 通知装配助手测试（2026-10-24；2026-10-26 随「同时通知」裁决语义反转）：
// dispatcher 构建即含可用渠道存储、持久化跨重开生效；waitNotifyIdle 对 nil
// 立即返回；三模式统一装配（旧 standalone 门控退役），serviceNotifier 的
// nil⇒真 nil 语义与 networked 的「同时通知」gate 在此锁定。
package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/localapi"
	"github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/notify"
	"github.com/mtl802/swarmmind/internal/protocol"
)

func TestOpenNotifyDispatcher(t *testing.T) {
	dir := t.TempDir()
	d, err := openNotifyDispatcher(dir)
	if err != nil {
		t.Fatalf("打开渠道存储失败: %v", err)
	}
	if d == nil {
		t.Fatal("装配应得非 nil dispatcher")
	}
	if _, err := d.Store().Create(notify.TypeWebhook, notify.FormatFeishu, "测试群", nil,
		map[string]string{"url": "https://open.feishu.cn/open-apis/bot/v2/hook/x"}, time.Now()); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	// 重开同目录：持久化生效（重启渠道不丢）
	d2, err := openNotifyDispatcher(dir)
	if err != nil {
		t.Fatalf("重开渠道存储失败: %v", err)
	}
	if len(d2.Store().List()) != 1 {
		t.Fatalf("重开后应见 1 条渠道，got %d", len(d2.Store().List()))
	}
	d.WaitIdle()
	d2.WaitIdle()
}

func TestWaitNotifyIdleNilSafe(t *testing.T) {
	done := make(chan struct{})
	go func() {
		waitNotifyIdle(nil, time.Second) // 旧构造方 nil：立即返回
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitNotifyIdle(nil) 未立即返回")
	}
}

// TestNotifyDispatcherForAllModes 三模式统一装配（2026-10-26「同时通知」裁决，
// 旧 standalone 门控退役）：三模式均非 nil 可用；serviceNotifier 的 nil⇒真 nil
// 语义保持锁定（typed-nil 防线不随门控退役而撤）。standalone 直连、networked
// 包 gate、setup 直连。
func TestNotifyDispatcherForAllModes(t *testing.T) {
	for _, m := range []string{localapi.ModeSetup, localapi.ModeNetworked, localapi.ModeStandalone} {
		d, err := notifyDispatcherFor(t.TempDir())
		if err != nil {
			t.Fatalf("%s 装配失败: %v", m, err)
		}
		if d == nil {
			t.Fatalf("%s 应装配非 nil dispatcher（三模式统一）", m)
		}
		// 非 nil 侧镜像：接口赋值后可用（无渠道零投递，不 panic）。
		if notif := serviceNotifier(d, m, t.TempDir()); notif == nil {
			t.Fatalf("%s 通知接口不应为 nil", m)
		} else {
			notif.TaskFinal(protocol.Task{ID: "t-gate", Status: protocol.StatusCompleted})
		}
		d.WaitIdle()
	}
	if notif := serviceNotifier(nil, localapi.ModeNetworked, t.TempDir()); notif != nil {
		t.Fatal("nil dispatcher 须返回真 nil 接口（typed-nil 防线）")
	}
}

// TestServiceNotifierGateSemantics networked「同时通知」开关语义（读盘即时生效，
// 默认 true=跟发；false=本机静默）：codex 审查建议修——此前只锁开关读写路径，
// 未证 gate 真的拦投递。此处补真实门控断言（httptest webhook 计数触达）：
//   - 未配置（默认 true）→ 发；写 false 即刻不发（读盘不缓存）；改回 true 即刻发；
//   - standalone 忽略开关值（false 也照发——本机通知即唯一通知）；
//   - 配置文件损坏 → 读失败回退 true（配置损坏不吞通知）。
func TestServiceNotifierGateSemantics(t *testing.T) {
	dir := t.TempDir()
	// 开关读写路径：未配置默认 true、false 即刻可读、改回即刻生效（读盘不缓存）
	if !notify.AlsoNotify(dir) {
		t.Fatal("未配置开关应默认 true（跟发）")
	}
	if err := notify.SetAlsoNotify(dir, false); err != nil {
		t.Fatalf("写开关失败: %v", err)
	}
	if notify.AlsoNotify(dir) {
		t.Fatal("false 应即刻读到（读盘不缓存）")
	}
	if err := notify.SetAlsoNotify(dir, true); err != nil {
		t.Fatalf("写回开关失败: %v", err)
	}
	if !notify.AlsoNotify(dir) {
		t.Fatal("改回 true 应即刻生效")
	}

	// 真实门控：webhook 渠道打 httptest 计数器，gate 拦截与否看投递触达
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	d, err := openNotifyDispatcher(dir)
	if err != nil {
		t.Fatalf("打开渠道存储失败: %v", err)
	}
	if _, err := d.Store().Create(notify.TypeWebhook, notify.FormatGeneric, "门控测试", nil,
		map[string]string{"url": srv.URL}, time.Now()); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	task := protocol.Task{ID: "t-gate", Status: protocol.StatusCompleted}
	netNotif := serviceNotifier(d, localapi.ModeNetworked, dir)
	saNotif := serviceNotifier(d, localapi.ModeStandalone, dir)
	hitsOf := func() int { mu.Lock(); defer mu.Unlock(); return hits }
	fire := func(n service.TerminalNotifier, wantDelta int, why string) {
		t.Helper()
		before := hitsOf()
		n.TaskFinal(task)
		d.WaitIdle()
		if got := hitsOf() - before; got != wantDelta {
			t.Fatalf("%s: 期望 %d 次投递，实得 %d", why, wantDelta, got)
		}
	}
	fire(netNotif, 1, "networked 未配置（默认 true）应跟发")
	if err := notify.SetAlsoNotify(dir, false); err != nil {
		t.Fatalf("写开关失败: %v", err)
	}
	fire(netNotif, 0, "networked 关闭后本机渠道不得发（hub 一份照发，不在本断言面）")
	if err := notify.SetAlsoNotify(dir, true); err != nil {
		t.Fatalf("写回开关失败: %v", err)
	}
	fire(netNotif, 1, "networked 改回 true 即刻恢复跟发")
	if err := notify.SetAlsoNotify(dir, false); err != nil {
		t.Fatalf("写开关失败: %v", err)
	}
	fire(saNotif, 1, "standalone 忽略开关值（false 也照发：本机通知即唯一通知）")
	if err := os.WriteFile(filepath.Join(dir, "notify_settings.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatalf("写损坏配置失败: %v", err)
	}
	if !notify.AlsoNotify(dir) {
		t.Fatal("配置损坏读失败应回退 true")
	}
	fire(netNotif, 1, "配置损坏回退 true 应照发（不吞通知）")
}
