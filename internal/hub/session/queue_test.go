package session

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// ---------- L5：发送队列通知分离与停滞时钟（与 node/conn 同构实现,两侧各测） ----------

// TestOutboundQBroadcastWakesAllProducers L5：多个阻塞生产者在数据出队后必须
// 全部及时唤醒（代次广播，杜绝容量 1 通知被生产者/消费者互抢导致丢唤醒）。
func TestOutboundQBroadcastWakesAllProducers(t *testing.T) {
	q := newOutboundQ()
	defer q.close(false)
	big := make([]byte, protocol.SendQueueLimit)
	if err := q.pushData(big, time.Second); err != nil {
		t.Fatalf("填充队列: %v", err)
	}
	const producers = 3
	res := make(chan error, producers)
	var entered sync.WaitGroup
	entered.Add(producers)
	one := []byte("x")
	for i := 0; i < producers; i++ {
		go func() {
			entered.Done()
			res <- q.pushData(one, 500*time.Millisecond)
		}()
	}
	entered.Wait()
	time.Sleep(50 * time.Millisecond) // 让生产者进入等待（正确性不依赖该时长）
	if b := q.pop(); b == nil {
		t.Fatal("应能取出大帧")
	}
	for i := 0; i < producers; i++ {
		select {
		case err := <-res:
			if err != nil {
				t.Fatalf("生产者 %d 应被广播唤醒并入队, got %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("生产者 %d 未被唤醒（丢唤醒）", i)
		}
	}
}

// TestOutboundQStallIgnoresCtrlActivity L5：控制帧周期入队（旧实现会 Reset 停滞
// 计时器）不得刷新数据停滞时钟——恰在 stall 时限断开。
func TestOutboundQStallIgnoresCtrlActivity(t *testing.T) {
	q := newOutboundQ()
	defer q.close(false)
	big := make([]byte, protocol.SendQueueLimit)
	if err := q.pushData(big, time.Second); err != nil {
		t.Fatalf("填充队列: %v", err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ping := []byte("p")
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				_ = q.pushCtrl(ping)
			}
		}
	}()
	const stall = 300 * time.Millisecond
	start := time.Now()
	err := q.pushData([]byte("y"), stall)
	elapsed := time.Since(start)
	if !errors.Is(err, errDataStall) {
		t.Fatalf("应 errDataStall, got %v", err)
	}
	if elapsed >= stall+250*time.Millisecond {
		t.Fatalf("控制帧活动不得刷新停滞时钟（应在 %s 附近断开）, took %s", stall, elapsed)
	}
}

// TestOutboundQGraceDowngrade L3/L4 配套：已优雅关闭的队列可降级为非优雅
//（预算耗尽强断路径），写 goroutine 退出路径随之改变。
func TestOutboundQGraceDowngrade(t *testing.T) {
	q := newOutboundQ()
	q.close(true)
	if closed, grace := q.state(); !closed || !grace {
		t.Fatalf("应 closed+graceful, got %v %v", closed, grace)
	}
	if !q.graceClosed() {
		t.Fatal("graceClosed 应为 true")
	}
	q.close(false) // 降级
	if closed, grace := q.state(); !closed || grace {
		t.Fatalf("降级后应 closed+非优雅, got %v %v", closed, grace)
	}
	if q.graceClosed() {
		t.Fatal("降级后 graceClosed 应为 false")
	}
}
