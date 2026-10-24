// standalone 通知装配助手测试（2026-10-24）：dispatcher 构建即含可用渠道存储、
// 持久化跨重开生效；waitNotifyIdle 对 nil（networked/setup 口径）立即返回；
// 形态门控收口在 notifyDispatcherFor（2026-10-24 返工必修1）——networked/setup
// 返回真 nil，杜绝 nil 具体指针装进 TerminalNotifier 接口成 typed-nil。
package main

import (
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/node/localapi"
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
		t.Fatal("standalone 装配应得非 nil dispatcher")
	}
	if _, err := d.Store().Create(notify.TypeFeishu, "测试群", nil,
		map[string]string{"webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/x"}, time.Now()); err != nil {
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
		waitNotifyIdle(nil, time.Second) // networked/setup：nil dispatcher 立即返回
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitNotifyIdle(nil) 未立即返回")
	}
}

// TestNotifyDispatcherForModeGating 装配门控（返工必修1/5）：networked/setup 返回
// 真 nil 零装配（通知归 hub / 无 agents），standalone 非 nil 可用。旧缺陷形态——
// nil *notify.Dispatcher 直接赋给 TerminalNotifier 接口成 typed-nil（接口非 nil，
// networked 终态仍读账本调 TaskFinal，违反「networked 零调用」承诺）——serviceNotifier
// 即 run() 内 svcCfg.Notifier 的装配函数，直接锁定其 nil⇒真 nil 语义。
func TestNotifyDispatcherForModeGating(t *testing.T) {
	for _, m := range []string{localapi.ModeSetup, localapi.ModeNetworked} {
		d, err := notifyDispatcherFor(m, t.TempDir())
		if err != nil {
			t.Fatalf("%s 装配失败: %v", m, err)
		}
		if d != nil {
			t.Fatalf("%s 不应装配通知（零调用承诺）", m)
		}
		if notif := serviceNotifier(d); notif != nil {
			t.Fatalf("%s 通知接口为 typed-nil（非真 nil），终态路径会空跑调用", m)
		}
	}
	d, err := notifyDispatcherFor(localapi.ModeStandalone, t.TempDir())
	if err != nil {
		t.Fatalf("standalone 装配失败: %v", err)
	}
	if d == nil {
		t.Fatal("standalone 应装配非 nil dispatcher")
	}
	// 非 nil 侧镜像：接口赋值后可用（无渠道零投递，不 panic）。
	if notif := serviceNotifier(d); notif == nil {
		t.Fatal("standalone 通知接口不应为 nil")
	} else {
		notif.TaskFinal(protocol.Task{ID: "t-gate", Status: protocol.StatusCompleted})
	}
	d.WaitIdle()
}
