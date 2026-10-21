package conn

import (
	"context"
	"math/rand"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// Backoff 重连指数退避（SPEC 4.6：0.5s 起 ×2 ±20% 抖动，上限 60s；
// 注册成功后重置，auth_failed 不重试——不重试由调用方以 ErrAuthFailed 终止）。
// 非并发安全：仅重连循环单 goroutine 使用。
type Backoff struct {
	initial time.Duration
	max     time.Duration
	jitter  float64
	cur     time.Duration
}

// NewBackoff 构造退避器；参数非正数回落协议常量。
func NewBackoff(initial, max time.Duration, jitter float64) *Backoff {
	if initial <= 0 {
		initial = protocol.BackoffInitial
	}
	if max <= 0 {
		max = protocol.BackoffMax
	}
	if jitter <= 0 || jitter >= 1 {
		jitter = protocol.BackoffJitter
	}
	return &Backoff{initial: initial, max: max, jitter: jitter, cur: initial}
}

// Next 返回下次重连等待时长：在上次基础上 ×2（首次即 initial），加 ±jitter
// 均匀抖动，封顶 max。
func (b *Backoff) Next() time.Duration {
	cur := b.cur
	b.cur *= 2
	if b.cur > b.max {
		b.cur = b.max
	}
	// ±20% 均匀抖动：[1-j, 1+j]
	factor := 1 - b.jitter + 2*b.jitter*rand.Float64()
	d := time.Duration(float64(cur) * factor)
	if d > b.max {
		d = b.max
	}
	if d < 0 {
		d = 0
	}
	return d
}

// Reset 注册成功后重置回起始间隔（SPEC 4.6）。
func (b *Backoff) Reset() { b.cur = b.initial }

// Sleep 休眠 d；ctx 取消即提前返回。
func Sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
