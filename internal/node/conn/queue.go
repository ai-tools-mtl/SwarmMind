package conn

import (
	"errors"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 会话发送队列错误（errors.Is 判定）。
var (
	// errQueueClosed 连接已关闭后再入队。
	errQueueClosed = errors.New("conn: 发送队列已关闭")
	// errCtrlOverflow 控制帧小缓冲也满 → 视为对端已死，断开重连（SPEC 4.6）。
	errCtrlOverflow = errors.New("conn: 控制帧缓冲已满")
	// errDataStall 数据队列持续无法出队 → 断开重连（补推幂等，SPEC 4.6）。
	errDataStall = errors.New("conn: 数据队列出队停滞")
)

// outboundQ 连接发送队列（SPEC 4.6，与 hub session 同构：node 会话同受
// 4MiB/64KiB/60s 约束）。websocket 单连接写必须串行化（SPEC 第九节）：
// 唯一消费方是本连接的专职写 goroutine。与 hub/session 的实现刻意各自维护——
// SPEC 三目录树中两包互不依赖，避免 node 反向 import hub。
//
// 通知纪律（L5，与 hub session 同口径）：dataReady（容量 1）只唤醒写 goroutine；
// spaceGen 代次广播唤醒全部阻塞中的 pushData；停滞判定用固定截止时间 + 数据
// 出队代次，控制帧活动不刷新数据停滞时钟。
type outboundQ struct {
	mu        sync.Mutex
	ctrl      [][]byte
	data      [][]byte
	ctrlB     int
	dataB     int
	dataReady chan struct{} // 容量 1：有帧入队（写 goroutine 唤醒，可合并）
	spaceGen  chan struct{} // 代次广播：数据空位变化（每次出队换代）
	drained   int64         // 数据帧出队累计代次（停滞时钟的进展基准）
	done      chan struct{} // close 即队列死亡（连接关闭）
	closed    bool
	grace     bool
}

func newOutboundQ() *outboundQ {
	return &outboundQ{
		dataReady: make(chan struct{}, 1),
		spaceGen:  make(chan struct{}),
		done:      make(chan struct{}),
	}
}

func (q *outboundQ) signalData() {
	select {
	case q.dataReady <- struct{}{}:
	default:
	}
}

// broadcastSpace 数据空位已变化：close 旧广播通道唤醒全部等待中的 pushData 并
// 换代。须持 q.mu 调用。
func (q *outboundQ) broadcastSpace() {
	close(q.spaceGen)
	q.spaceGen = make(chan struct{})
	q.drained++
}

// spaceWait 返回当前空位广播通道。须持 q.mu 调用。
func (q *outboundQ) spaceWait() chan struct{} {
	return q.spaceGen
}

// pushCtrl 控制帧入队：64KiB 小缓冲，满即置死队列（对端已死）并报错。
func (q *outboundQ) pushCtrl(b []byte) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return errQueueClosed
	}
	if q.ctrlB+len(b) > protocol.ControlQueueLimit {
		q.closed = true
		q.grace = false
		close(q.done)
		q.mu.Unlock()
		return errCtrlOverflow
	}
	q.ctrl = append(q.ctrl, b)
	q.ctrlB += len(b)
	q.mu.Unlock()
	q.signalData()
	return nil
}

// pushHandshake 注册握手帧入队（N1）：register 携完整能力表，合法上限同信封
// 1MiB，不受 64KiB 控制小缓冲约束；调用前已按最终编码实测预检（attempt）。
// 此刻连接刚建立、队列必空，与数据帧同队串行写出，顺序不受影响。
func (q *outboundQ) pushHandshake(b []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return errQueueClosed
	}
	if len(b) > protocol.MaxMessageBytes {
		return errQueueClosed // 防御：attempt 已预检，不可达
	}
	q.data = append(q.data, b)
	q.dataB += len(b)
	q.signalData()
	return nil
}

// pushData 数据帧入队：满则阻塞等待空位（背压）。停滞判定（L5）：截止时间只在
// 观察到数据出队进展（drained 代次推进）时顺延，控制帧活动不刷新时钟。
func (q *outboundQ) pushData(b []byte, stall time.Duration) error {
	timer := time.NewTimer(stall)
	defer timer.Stop()
	deadline := time.Now().Add(stall)
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return errQueueClosed
		}
		if q.dataB+len(b) <= protocol.SendQueueLimit {
			q.data = append(q.data, b)
			q.dataB += len(b)
			q.mu.Unlock()
			q.signalData()
			return nil
		}
		waitCh := q.spaceWait()
		seen := q.drained
		q.mu.Unlock()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(time.Until(deadline))
		select {
		case <-waitCh: // 空位变化（真有数据出队），重检
		case <-q.done:
			// 回环重检 closed，给出确定性错误
		case <-timer.C:
			q.close(false)
			return errDataStall
		}
		q.mu.Lock()
		progressed := q.drained != seen
		q.mu.Unlock()
		if progressed {
			deadline = time.Now().Add(stall)
		}
	}
}

// pop 取下一帧：控制帧优先；都空返回 nil。数据帧出队释放空位并广播唤醒全部
// 阻塞中的 pushData（控制帧无等待生产者，不广播）。
func (q *outboundQ) pop() []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.ctrl) > 0 {
		b := q.ctrl[0]
		q.ctrl[0] = nil
		q.ctrl = q.ctrl[1:]
		q.ctrlB -= len(b)
		return b
	}
	if len(q.data) > 0 {
		b := q.data[0]
		q.data[0] = nil
		q.data = q.data[1:]
		q.dataB -= len(b)
		q.broadcastSpace()
		return b
	}
	return nil
}

// close 置死队列；幂等。对已关闭队列可用 graceful=false 降级为非优雅
//（尚未进入排空的写 goroutine 改走 CloseNow）。
func (q *outboundQ) close(graceful bool) {
	q.mu.Lock()
	if q.closed {
		if !graceful {
			q.grace = false
		}
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.grace = graceful
	close(q.done)
	q.mu.Unlock()
}

func (q *outboundQ) state() (closed, graceful bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed, q.grace
}

func (q *outboundQ) drainCtrl() [][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.ctrl
	q.ctrl = nil
	q.ctrlB = 0
	return out
}
