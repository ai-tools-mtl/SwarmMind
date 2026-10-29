package sessionx

// sessionGate 同会话单写者门（R1 审查 R4 修订）：以 FIFO 队列实现「同会话轮次严格
// 串行、后到者排队等待不拒绝」，且等待可响应 ctx 取消/到期——sync.Mutex.Lock 不可
// 取消，前轮长跑会吞掉后轮的截止期。弃队者不持有写权、不得 spawn、不消耗轮号
//（Manager.Send 在 acquire 失败时直接返回 ctx 错误，不触会话状态）。

import (
	"context"
	"sync"
)

type sessionGate struct {
	mu      sync.Mutex
	held    bool            // 写权是否已被持有（含在途移交：交给队首后仍为 true）
	waiters []chan struct{} // FIFO 排队者；release 关闭队首通道即移交写权
}

// acquire 按到达顺序取得写权；ctx 取消/到期即弃队返回错误。返回的释放函数必须由
// 持有者调用（defer release()），不得重复调用。
func (g *sessionGate) acquire(ctx context.Context) (func(), error) {
	g.mu.Lock()
	if !g.held && len(g.waiters) == 0 {
		g.held = true
		g.mu.Unlock()
		return g.release, nil
	}
	ch := make(chan struct{})
	g.waiters = append(g.waiters, ch)
	g.mu.Unlock()

	select {
	case <-ch:
		return g.release, nil
	case <-ctx.Done():
		g.mu.Lock()
		granted := true
		for i, w := range g.waiters {
			if w == ch {
				g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
				granted = false
				break
			}
		}
		g.mu.Unlock()
		if granted {
			// 弃队与中签竞态（select 随机选中 Done，写权已实际移交本调用者）：
			// 立即转交下一位或归还空门，绝不带着写权返回取消错误。
			g.release()
		}
		return nil, ctx.Err()
	}
}

// release 归还写权：有排队者则移交队首（close 通道即中签），否则门回到空闲。
func (g *sessionGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.waiters) > 0 {
		next := g.waiters[0]
		g.waiters = g.waiters[1:]
		close(next)
		return
	}
	g.held = false
}
