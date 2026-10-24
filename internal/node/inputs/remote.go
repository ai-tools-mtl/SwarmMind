// 跨机取回解析器（P4④第二子轮，FILE-COLLAB L2 / SPEC 4.12）：下游 node 的本机
// 账本无依赖 idem 的 completed 上游时，经 hub 中继取回异机上游产物——先
// dep_resolve 定位上游任务与产物清单（hub 按 resolveIDem 同口径解析），再逐件
// artifact_fetch 取回字节流（hub 转发回源 node，源 node 校验 SHA 后回帧）。
// 取回字节进入与 LocalResolver 完全相同的挂载事务（UpstreamInputs.Fetch 字节
// 源缝），SHA 校验/暂存/原子发布/回滚语义零分叉。
//
// 失败与离线语义（FILE-COLLAB 契约）：
//   - 源 node 离线/中继不可达/中继超时 → 「暂不可取」（ErrRemoteRetryable），
//     在准备阶段独立超时内按 RetryInterval 重试轮询——源恢复即继续挂载，穷尽
//     后 input_prepare_failed 不 spawn；
//   - 上游产物过保留期已被清理 / 无 completed 上游 / 越权 → 「已清理/不存在」
//     （ErrRemoteGone）与校验不符（ErrRemoteMismatch）均为永久失败，立即返回
//     明确错误文案，不静默跳过、不重试。
package inputs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 远端取回节奏默认值：单次尝试等待 AttemptTimeout（与 hub 中继取回超时 30s 对
// 齐——正常必有确定应答，此处兜底帧丢失/重连场景）；重试轮询间隔 RetryInterval
//（FILE-COLLAB 建议 5-10s 取下限）。两者均可经 RemoteResolver 字段覆盖（service
// 装配注入），准备阶段总预算仍由调用方独立超时（InputPrepareTimeout）收口。
const (
	DefaultRetryInterval  = 5 * time.Second
	DefaultAttemptTimeout = 30 * time.Second
)

// 远端取回失败分类（RemoteFetch 实现以 errors.Is 语义包装返回；RemoteResolver
// 据此决定重试或立即失败）。
var (
	// ErrRemoteRetryable 暂不可取：源 node 离线、hub 不可达、中继超时、传输
	// 失败——重试轮询可能恢复。
	ErrRemoteRetryable = errors.New("上游暂不可取（源离线或中继不可达）")
	// ErrRemoteGone 已清理/不存在：源 node 产物过保留期、上游无 completed 版本
	// 或依赖归属越权——永久失败，重试无意义。
	ErrRemoteGone = errors.New("上游产物不存在（已过保留期清理或无 completed 上游）")
	// ErrRemoteMismatch 完整性不符：取回内容与登记 SHA-256/大小不一致——永久
	// 失败（中继链完整性校验失败）。
	ErrRemoteMismatch = errors.New("取回内容与登记不一致（SHA-256/大小）")
)

// DepTarget dep_resolve 的解析结果：上游任务定位与产物清单（hub 脱敏 Task 的
// 提取——只含挂载所需字段，不含上游输入等内容隐私）。
type DepTarget struct {
	// TaskID 满足依赖 idem 的上游任务 ID（hub 最早 completed 口径）。
	TaskID string
	// TargetNode 上游任务执行所在 node（hub 中继取回的路由目标；仅供诊断）。
	TargetNode string
	// Artifacts 上游产物登记清单（Path/SHA-256/Size）。空 = 答案型上游。
	Artifacts []protocol.Artifact
}

// RemoteFetch 跨机取回的帧通道抽象（service 侧经 dep_resolve/artifact_fetch
// 实现一次尝试；重试节奏、失败分类的消费与挂载事务在 RemoteResolver）。实现
// 须把帧层错误归类为三类哨兵（errors.Is 可判定），未知错误按可重试处理。
type RemoteFetch interface {
	// ResolveDep 解析依赖 idem → 上游任务（一次尝试）。ctx 为单次尝试预算
	//（超时/取消 → ErrRemoteRetryable）。
	ResolveDep(ctx context.Context, idem string) (*DepTarget, error)
	// FetchArtifact 取回上游任务第 index 个产物字节（一次尝试）。返回前须对照
	// meta 完成 SHA-256 与大小校验（不符 → ErrRemoteMismatch）。
	FetchArtifact(ctx context.Context, taskID string, index int, meta protocol.Artifact) ([]byte, error)
}

// RemoteResolver 跨机上游解析器（InputResolver 第二实现）：dep_resolve/artifact_fetch
// 通道 + 重试轮询。零值不可用——Fetch 必填，节奏字段缺省回退包默认值。
type RemoteResolver struct {
	// Fetch 帧通道（service 装配注入；单测注入 mock 中继）。
	Fetch RemoteFetch
	// RetryInterval 重试轮询间隔；0 = DefaultRetryInterval。
	RetryInterval time.Duration
	// AttemptTimeout 单次尝试等待预算；0 = DefaultAttemptTimeout。
	AttemptTimeout time.Duration
}

// interval / attempt 节奏取值（字段缺省回退包默认）。
func (r *RemoteResolver) interval() time.Duration {
	if r.RetryInterval > 0 {
		return r.RetryInterval
	}
	return DefaultRetryInterval
}

func (r *RemoteResolver) attempt() time.Duration {
	if r.AttemptTimeout > 0 {
		return r.AttemptTimeout
	}
	return DefaultAttemptTimeout
}

// ResolveUpstream 解析单个依赖 idem（InputResolver 实现）：dep_resolve 重试轮询
// 至成功或准备预算穷尽 → 上游定位与产物清单 → 返回带 Fetch 字节源的
// UpstreamInputs（逐件取回同样重试轮询 + SHA/大小校验，在挂载事务暂存阶段
// 逐件调用）。永久失败（已清理/不符）不重试立即返回。
func (r *RemoteResolver) ResolveUpstream(ctx context.Context, idem string) (*UpstreamInputs, error) {
	t, err := retry(ctx, r, "依赖解析", func(ctx context.Context) (*DepTarget, error) {
		return r.Fetch.ResolveDep(ctx, idem)
	})
	if err != nil {
		return nil, decorateRemoteErr(err, decorate{IDem: idem})
	}
	if len(t.Artifacts) == 0 {
		// 答案型上游（completed 零产物登记）：无内容可挂，不建目录不算失败
		//（与 LocalResolver 口径一致）。
		return &UpstreamInputs{TaskID: t.TaskID}, nil
	}
	arts := append([]protocol.Artifact(nil), t.Artifacts...)
	up := &UpstreamInputs{TaskID: t.TaskID, Artifacts: arts}
	up.Fetch = func(ctx context.Context, art protocol.Artifact) ([]byte, error) {
		idx := -1
		for i := range arts {
			if arts[i].Path == art.Path && arts[i].Sha256 == art.Sha256 {
				idx = i
				break
			}
		}
		if idx < 0 {
			// 清单自身不一致（不应发生：art 恒来自 arts 枚举）：永久失败
			return nil, &Error{IDem: idem, Upstream: t.TaskID, Path: art.Path,
				Reason: "产物不在上游登记清单内（内部一致性防御）"}
		}
		data, rerr := retry(ctx, r, "产物取回", func(ctx context.Context) ([]byte, error) {
			data, err := r.Fetch.FetchArtifact(ctx, t.TaskID, idx, art)
			if err != nil {
				return nil, err
			}
			// 取回链完整性终检（hub 中继与源 node 已各校验一次，三重保险）：
			// 大小与 SHA-256 对照登记，不符即永久失败。
			if int64(len(data)) != art.Size {
				return nil, fmt.Errorf("%w: 产物 %s（登记 %d 字节，实得 %d）",
					ErrRemoteMismatch, art.Path, art.Size, len(data))
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != strings.ToLower(art.Sha256) {
				return nil, fmt.Errorf("%w: 产物 %s（登记 %s，实得 %s）",
					ErrRemoteMismatch, art.Path, art.Sha256, hex.EncodeToString(sum[:]))
			}
			return data, nil
		})
		if rerr != nil {
			return nil, decorateRemoteErr(rerr, decorate{Upstream: t.TaskID, Path: art.Path})
		}
		return data, nil
	}
	return up, nil
}

// decorate 错误定位补齐（*Error 缺省字段）。
type decorate struct {
	IDem     string
	Upstream string
	Path     string
}

// decorateRemoteErr 为取回链错误补齐上游/文件定位（SPEC 4.12 失败语义：文案
// 注明上游任务与文件）；非 *Error 错误包装成 *Error。
func decorateRemoteErr(err error, d decorate) error {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Reason: err.Error()}
	}
	if e.IDem == "" {
		e.IDem = d.IDem
	}
	if e.Upstream == "" {
		e.Upstream = d.Upstream
	}
	if e.Path == "" {
		e.Path = d.Path
	}
	return e
}

// retry 重试轮询骨架：单次尝试以 attempt() 为预算（attempt ctx 取调用方 ctx
// 子集，随准备超时/取消一并打断）；可重试失败（ErrRemoteRetryable）按
// interval() 间隔轮询直至调用方预算穷尽；永久失败（ErrRemoteGone/
// ErrRemoteMismatch）立即返回；其余未知错误按可重试处理（预算兜底）。
// what 用于错误文案定位（依赖解析/产物取回）。自由函数——Go 方法不得带类型
// 参数。
func retry[T any](ctx context.Context, r *RemoteResolver, what string, fn func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	for {
		actx, cancel := context.WithTimeout(ctx, r.attempt())
		res, err := fn(actx)
		cancel()
		if err == nil {
			return res, nil
		}
		if errors.Is(err, ErrRemoteGone) || errors.Is(err, ErrRemoteMismatch) {
			var e *Error
			if errors.As(err, &e) {
				return zero, e // Fetch 实现已构造定位完整的 *Error
			}
			return zero, &Error{Reason: what + "永久失败: " + err.Error()}
		}
		if ctx.Err() != nil {
			// 准备预算穷尽/中止：按独立超时语义收敛（调用方不得 MarkStarted）
			te := timeoutErr(ctx.Err())
			te.Reason = what + "：" + te.Reason + "（" + err.Error() + "）"
			return zero, te
		}
		select {
		case <-ctx.Done(): // 轮询等待中被打断：同上收敛
			te := timeoutErr(ctx.Err())
			te.Reason = what + "：" + te.Reason + "（" + err.Error() + "）"
			return zero, te
		case <-time.After(r.interval()):
		}
	}
}

// Resolver 组合解析器（装配方使用）：本机优先——账本有 completed 上游走本地
// 复制（行为零变化）；本机无（RemoteCandidate）且 Remote 非 nil 转远端取回。
// standalone（Remote=nil）行为与第一子轮完全一致。
type Resolver struct {
	Local  *LocalResolver
	Remote *RemoteResolver // nil = 禁用跨机取回（standalone/setup）
}

// ResolveUpstream 解析单个依赖 idem（InputResolver 实现）：Local → 命中返回；
// RemoteCandidate 失败且 Remote 可用 → Remote 兜底；其余失败原样返回。
func (r *Resolver) ResolveUpstream(ctx context.Context, idem string) (*UpstreamInputs, error) {
	up, err := r.Local.ResolveUpstream(ctx, idem)
	if err == nil {
		return up, nil
	}
	var e *Error
	if !errors.As(err, &e) || !e.RemoteCandidate || r.Remote == nil {
		return nil, err
	}
	return r.Remote.ResolveUpstream(ctx, idem)
}
