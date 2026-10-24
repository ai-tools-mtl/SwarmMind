// Package inputs 实现 FILE-COLLAB L2 第二子批第一子轮的产物挂载骨架（SPEC 4.12）：
// 依赖门控放行的下游任务在 started/spawn 前，把全部上游任务的产物**纯复制**到
// 消费者专属输入区 <下游workdir>/inputs/<上游task_id>/，逐文件 SHA-256 校验后
// 整目录 rename 原子发布。
//
// 纪律（FILE-COLLAB L2 终审口径 + codex 审查加固，2026-10-24）：
//   - 纯复制，禁用硬链接——下游对挂载件的任何改动不得污染上游产物 SHA（硬链接
//     会让 artifact 校验必炸）；
//   - 文件操作安全：输入区基目录 Lstat 校验（符号链接/非常规目录一律拒绝，防
//     MkdirAll 跟随 inputs 符号链接逃逸 workdir），inputs 域内全部盘上操作经
//     os.Root 定界（openat 风格，越界路径不可达）；上游源文件整条路径逐段 Lstat
//     （符号链接不跟随）+ 末段 O_NOFOLLOW|O_NONBLOCK 打开（内核在 open 原语上
//     强制不跟随、不阻塞，消 Lstat→Open 换入链接/FIFO 的窗口）+ 打开后 fd fstat
//     确认 regular file——校验的对象与读取的对象是同一 fd；复制以登记大小+1 为
//     字节上限（防源在检查后增长），复制后核对实得字节数==登记大小；
//   - 输入就绪前不 spawn：准备阶段独立超时（DefaultTimeout，调用方经 ctx 注入，
//     独立于任务执行超时）；ctx 贯穿复制/校验/发布全程，末次发布后再查一次——
//     超时/取消后 Prepare 必返回失败，调用方不得 MarkStarted；
//   - 原子发布（journal 事务）：暂存 → 发布前先落 journal（发布意图+步骤状态，
//     原子写），逐上游「移开旧目录→rename 发布→journal 状态推进」紧凑执行并
//     目录 fsync；单上游自身原子，多上游按声明序逐个（整体跨上游非原子，SPEC
//     4.12 声明）；崩溃后重挂先按 journal 回放收敛（完成未竟 rename 或归位旧
//     目录）再挂载；任一校验/发布失败尽力回滚，回滚自身失败则保留 trash/暂存
//     材料并聚合报告（哪个上游旧目录需人工恢复），输入区保持挂载前原状；
//   - 同 idem 多上游版本解析为账本内最早 completed（与 hub resolveIDem 口径
//     一致）；同任务命中多个 idem 时去重只挂一次。
//
// 跨机取回（P4④第二子轮，2026-10-24）：RemoteResolver 经 dep_resolve/artifact_fetch
// 帧通道（hub 中继回源 node）取回产物字节流，进入与本机路径完全相同的挂载事务；
// Resolver 组合解析器本机优先、账本无 completed 上游时转远端（remote.go）。
package inputs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// DefaultTimeout 输入准备默认独立超时：同机纯复制至多 8 上游 × 8 件 × 640KiB
//（≤32MiB），60s 上限宽裕；调用方（service）可经 Config 覆盖。
const DefaultTimeout = 60 * time.Second

// stagingPrefix 暂存目录前缀（inputs/ 内，保证 rename 同文件系统；点前缀避开
// 产物/输入的正常命名空间）。发布成功或失败回滚完成后整目录清理。
const stagingPrefix = ".staging-"

// inputsRel / journalName：输入区基目录与发布事务日志（相对 workdir、slash 口径）。
const (
	inputsRel   = "inputs"
	journalName = "journal.json"
)

// journal 步骤状态机：pending（已登记发布意图）→ old_moved（旧目录已移入
// trash）→ published（新目录已发布）。
const (
	stepPending   = "pending"
	stepOldMoved  = "old_moved"
	stepPublished = "published"
)

// UpstreamInputs 单个上游任务的产物解析结果（InputResolver 返回）。
type UpstreamInputs struct {
	// TaskID 满足依赖 idem 的上游任务 ID（挂载目录名 inputs/<TaskID>/）。
	TaskID string
	// Workdir 上游任务 workdir（本机绝对路径；产物 Path 的解析基准）。远端形态
	//（字节流取回）为空——产物内容经 Fetch 供给，不走本机 outputs 目录。
	Workdir string
	// Artifacts 上游结果登记的产物清单（Path 相对上游 workdir，outputs/ 口径）。
	// 空 = 上游 completed 但无产物登记（答案型上游）：不建目录、不算失败。
	Artifacts []protocol.Artifact
	// Fetch 远端产物字节源（P4④第二子轮跨机取回；nil = 本机 outputs 目录读取，
	// Local 路径零变化）。实现须在返回前对照登记完成 SHA-256 与大小校验，字节
	// 数恰为登记 Size 且 ≤ ArtifactLimit；失败返回 *Error（文案带 input_prepare_failed
	// 前缀）。取回在挂载事务的暂存阶段逐件调用——源离线在准备阶段内重试轮询
	//（RemoteResolver），事务原子性/回滚语义与本地路径完全一致。
	Fetch func(ctx context.Context, art protocol.Artifact) ([]byte, error)
}

// InputResolver 上游产物解析抽象（FILE-COLLAB L2 跨机取回的扩展缝）：按依赖
// idem 解析上游任务产物位置与内容来源。实现：LocalResolver（node 账本 + 本机
// outputs 目录）、RemoteResolver（dep_resolve/artifact_fetch 经 hub 中继取回）、
// Resolver（本机优先组合，装配方使用）。实现应返回 *Error（文案以
// protocol.CodeInputPrepareFailed 开头，供 TaskResult.Error 直接使用）。
type InputResolver interface {
	ResolveUpstream(ctx context.Context, idem string) (*UpstreamInputs, error)
}

// Error 输入准备失败（TaskResult.Error 载体）：文案恒以
// protocol.CodeInputPrepareFailed 前缀开头并注明上游任务/文件定位（SPEC 4.12
// 失败语义）。零值字段按需省略，Reason 必填。
type Error struct {
	IDem     string // 关联依赖 idem（解析失败时可能只有它）
	Upstream string // 上游任务 ID（解析成功后填充）
	Path     string // 相关产物相对路径（文件级失败时填充）
	Reason   string // 具体原因（缺失/SHA 不符/超限/发布失败/回滚未完全/超时）
	// RemoteCandidate 本机无该 idem 的 completed 上游（账本无条目/条目均未
	// completed），可尝试远端取回——Resolver 组合解析器据此决定是否转交
	// RemoteResolver（本机 agent 配置漂移等本地性失败不置位，错误文案即真相）。
	RemoteCandidate bool
}

// Error 实现 error。
func (e *Error) Error() string {
	var parts []string
	switch {
	case e.Upstream != "":
		parts = append(parts, "上游任务 "+e.Upstream)
	case e.IDem != "":
		parts = append(parts, "上游依赖 idem "+e.IDem)
	}
	if e.Path != "" {
		parts = append(parts, "产物 "+e.Path)
	}
	parts = append(parts, e.Reason)
	return protocol.CodeInputPrepareFailed + ": " + strings.Join(parts, " ")
}

// renameDir 目录/文件原子 rename 的包内缝（测试注入发布/归位失败用；生产恒
// os.Rename）。发布/回滚/journal 替换全部经此 choke point。
var renameDir = os.Rename

// Prepare 下游任务输入准备总入口：逐依赖（声明序）解析上游 → 同任务去重 →
// 暂存复制+逐文件 SHA-256 校验 → 原子发布。任一环节失败返回 *Error，输入区
// 保持挂载前原状；无上游（deps 空）或全部上游零产物时不产生任何目录。
func Prepare(ctx context.Context, resolver InputResolver, downstreamWorkdir, taskID string, deps []string) error {
	ups := make([]*UpstreamInputs, 0, len(deps))
	seen := make(map[string]bool, len(deps))
	for _, idem := range deps {
		up, err := resolver.ResolveUpstream(ctx, idem)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				e = &Error{IDem: idem, Reason: "上游解析失败: " + err.Error()}
			}
			if e.IDem == "" {
				e.IDem = idem
			}
			return e
		}
		if up == nil || len(up.Artifacts) == 0 {
			continue // 答案型上游（completed 无产物登记）：无内容可挂，不建目录
		}
		if seen[up.TaskID] {
			continue // 同一上游任务满足多个 idem（group_ref 多活等）：只挂一次
		}
		seen[up.TaskID] = true
		ups = append(ups, up)
	}
	if len(ups) == 0 {
		return nil
	}
	return mount(ctx, downstreamWorkdir, taskID, ups)
}

// ---------- 路径安全原语（os.Root 定界 + Lstat 不跟随） ----------

// absOf 相对路径（slash 口径）→ workdir 内绝对路径（renameDir 用：os.Root 无
// rename，rename 前须以 verifyDirRel 重验路径组件，见 mount 发布循环）。
func absOf(wroot *os.Root, rel string) string {
	return filepath.Join(wroot.Name(), filepath.FromSlash(rel))
}

// verifyDirRel Lstat（不跟随）核验对象为真实目录：符号链接/其他类型一律拒绝。
// inputs 域内每次路径型操作（rename 等）前重验路径组件，压缩校验→操作窗口。
func verifyDirRel(wroot *os.Root, rel, what string) error {
	fi, err := wroot.Lstat(rel)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s（%s）为符号链接或非常规目录，拒绝", rel, what)
	}
	return nil
}

// ensureInputsDir 输入区基目录安全建立：inputs 由本包创建/管理，校验用 Lstat
//（不跟随）——存在且为符号链接/非常规目录一律拒绝，防跟随 inputs 符号链接把
// 暂存/发布引到 workdir 之外；创建后再次校验（防御创建窗口内被替换的竞态）。
func ensureInputsDir(wroot *os.Root) error {
	_, err := wroot.Lstat(inputsRel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := wroot.Mkdir(inputsRel, 0o755); err != nil {
			return fmt.Errorf("创建 inputs 目录失败: %w", err)
		}
		return verifyDirRel(wroot, inputsRel, "输入区基目录")
	case err != nil:
		return err
	}
	return verifyDirRel(wroot, inputsRel, "输入区基目录")
}

// rootMkdirAll 逐段 Mkdir+Lstat 复验（os.Root 无 MkdirAll）：每段须为真实目录
//（已存在为符号链接即拒绝），防中途被替换越界。
func rootMkdirAll(wroot *os.Root, rel string) error {
	parts := strings.Split(rel, "/")
	for i := range parts {
		cur := strings.Join(parts[:i+1], "/")
		if err := wroot.Mkdir(cur, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := verifyDirRel(wroot, cur, "暂存路径段"); err != nil {
			return err
		}
	}
	return nil
}

// removeAllRel Root 定界的递归删除（os.Root 无 RemoveAll）：逐层 Lstat——仅对
// 真实目录递归，符号链接只删链接本身不跟随（防删除越界）。
func removeAllRel(wroot *os.Root, rel string) error {
	fi, err := wroot.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		ents, err := fs.ReadDir(wroot.FS(), rel)
		if err != nil {
			return err
		}
		for _, ent := range ents {
			if err := removeAllRel(wroot, rel+"/"+ent.Name()); err != nil {
				return err
			}
		}
	}
	return wroot.Remove(rel)
}

// syncDirRel 目录 fsync（rename 落盘持久化：先 rename 后 fsync 所在目录）。
func syncDirRel(wroot *os.Root, rel string) error {
	f, err := wroot.Open(rel)
	if err != nil {
		return err
	}
	serr := f.Sync()
	cerr := f.Close()
	if serr != nil {
		return serr
	}
	return cerr
}

// ---------- 发布事务 journal（崩溃恢复） ----------

// journalStep 单上游发布步骤状态。
type journalStep struct {
	Upstream string `json:"upstream"`
	State    string `json:"state"` // pending / old_moved / published
}

// journal 发布事务日志（暂存目录内 journalName）：发布阶段首个 rename 之前
// 先行落盘；崩溃后重挂按 journal+盘上实物回放收敛（recoverStaging）。
type journal struct {
	TaskID string        `json:"task_id"`
	Steps  []journalStep `json:"steps"`
}

// writeJournal 原子写 journal：临时文件写入+fsync 后 rename 替换，再 fsync
// 暂存目录（对齐仓库「临时文件+rename」原子写纪律）。
func writeJournal(wroot *os.Root, stageRel string, j *journal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmpRel := stageRel + "/" + journalName + ".tmp"
	f, err := wroot.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if serr != nil {
		return serr
	}
	if cerr != nil {
		return cerr
	}
	if err := renameDir(absOf(wroot, tmpRel), absOf(wroot, stageRel+"/"+journalName)); err != nil {
		return err
	}
	return syncDirRel(wroot, stageRel)
}

// readJournal 读 journal（不存在/损坏返回错误——调用方据此判定未进入发布阶段）。
func readJournal(wroot *os.Root, stageRel string) (*journal, error) {
	f, err := wroot.Open(stageRel + "/" + journalName)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var j journal
	if err := json.NewDecoder(f).Decode(&j); err != nil {
		return nil, err
	}
	return &j, nil
}

// recoverStaging 恢复同任务上次崩溃的发布残留：扫描 inputs/ 下
// .staging-<taskID>-* 暂存目录，按 journal+盘上实物逐步骤收敛——
//   - 无/坏 journal：崩溃发生在发布阶段前（journal 先于首个 rename 落盘），
//     输入区必然未被触碰，直接清理残缺暂存；
//   - 有 journal：目标已在=该上游已发布（journal 状态滞后无碍）；暂存内容在=
//     崩溃于「移开旧」与「发布新」之间，完成未竟发布（暂存内容进入发布阶段
//     前已全量校验，journal redo）；仅 trash 在=旧目录归位（journal 回滚）。
//     收敛后清理暂存（含 journal 与 trash）。
//
// 恢复失败残留材料原地保留（人工恢复），返回失败终止本次挂载。
func recoverStaging(wroot *os.Root, taskID string) error {
	ents, err := fs.ReadDir(wroot.FS(), inputsRel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return &Error{Reason: fmt.Sprintf("扫描输入区崩溃残留失败: %v", err)}
	}
	prefix := stagingPrefix + taskID + "-"
	for _, ent := range ents {
		if !ent.IsDir() || !strings.HasPrefix(ent.Name(), prefix) {
			continue // 符号链接/非常规条目不触碰（点前缀残留由人工处置）
		}
		stageRel := inputsRel + "/" + ent.Name()
		j, jerr := readJournal(wroot, stageRel)
		if jerr != nil {
			if rmErr := removeAllRel(wroot, stageRel); rmErr != nil {
				return &Error{Reason: fmt.Sprintf("清理无 journal 暂存残留失败（材料保留于 %s）: %v",
					absOf(wroot, stageRel), rmErr)}
			}
			continue
		}
		for _, st := range j.Steps {
			if err := recoverStep(wroot, stageRel, st); err != nil {
				return &Error{Upstream: st.Upstream,
					Reason: fmt.Sprintf("崩溃恢复未完成（trash/暂存材料保留于 %s，需人工恢复）: %v",
						absOf(wroot, stageRel), err)}
			}
		}
		if err := removeAllRel(wroot, stageRel); err != nil {
			return &Error{Reason: fmt.Sprintf("清理已恢复暂存残留失败（材料保留于 %s）: %v",
				absOf(wroot, stageRel), err)}
		}
	}
	return nil
}

// recoverStep 单步骤按盘上实物收敛（幂等：物理状态为准，可重放）。
func recoverStep(wroot *os.Root, stageRel string, st journalStep) error {
	dstRel := inputsRel + "/" + st.Upstream
	if _, err := wroot.Lstat(dstRel); err == nil {
		return nil // 目标已在：该上游发布已完成（含上次回滚归位的旧目录——重挂自会替换）
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	srcRel := stageRel + "/" + st.Upstream
	trashRel := stageRel + "/trash/" + st.Upstream
	_, srcErr := wroot.Lstat(srcRel)
	_, trashErr := wroot.Lstat(trashRel)
	if srcErr != nil && !errors.Is(srcErr, fs.ErrNotExist) {
		return srcErr
	}
	if trashErr != nil && !errors.Is(trashErr, fs.ErrNotExist) {
		return trashErr
	}
	if errors.Is(srcErr, fs.ErrNotExist) && errors.Is(trashErr, fs.ErrNotExist) {
		return nil // 两头皆无：该步骤未产生盘上效果
	}
	if err := verifyDirRel(wroot, inputsRel, "输入区基目录"); err != nil {
		return err
	}
	from := trashRel // 无暂存内容可发布：旧目录归位（journal 回滚）
	if srcErr == nil {
		from = srcRel // 暂存内容在：完成未竟发布（journal redo）
	}
	if err := renameDir(absOf(wroot, from), absOf(wroot, dstRel)); err != nil {
		return err
	}
	return syncDirRel(wroot, inputsRel)
}

// ---------- 挂载主流程 ----------

// mountPlan 单个上游的暂存→发布计划（rel 均 slash 口径、相对 workdir）。
type mountPlan struct {
	up       *UpstreamInputs
	srcRel   string // 暂存内容 inputs/.staging-…/<上游task_id>
	dstRel   string // 最终输入区 inputs/<上游task_id>
	trashRel string // 发布前旧目录移入 …/trash/<上游task_id>（回滚基准）
}

// mount 挂载核心：
//  0. workdir 建立与 inputs 基目录安全校验（防符号链接逃逸）+ 崩溃残留恢复；
//  1. 全量暂存复制+校验（不动输入区既有内容，失败只需清理暂存）；
//  2. 逐上游发布事务：journal 先行落盘 → 「移开旧目录→rename 发布→journal
//     推进」紧凑执行+目录 fsync——单上游自身原子，多上游按声明序逐个（整体
//     跨上游非原子，SPEC 4.12 声明）；
//  3. 任一失败尽力回滚（撤销已发布、旧目录归位、清理暂存）；回滚自身失败则
//     保留 trash/暂存/journal 材料并聚合报告；
//  4. 末次发布后再查 ctx——超时/取消后输入区不得视为就绪（调用方不得
//     MarkStarted），按失败语义回滚到挂载前原状。
func mount(ctx context.Context, workdir, taskID string, ups []*UpstreamInputs) error {
	// workdir 为配置管理路径（ArtifactRoots，与 runner exec 同源可信），缺失时
	// 建立之（历史行为：MkdirAll 暂存目录时顺带创建）；其内部 inputs 域才是
	// agent 可及面，全部操作经 os.Root 定界。
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return &Error{Reason: fmt.Sprintf("建立下游 workdir 失败: %v", err)}
	}
	wroot, err := os.OpenRoot(workdir)
	if err != nil {
		return &Error{Reason: fmt.Sprintf("打开下游 workdir 失败: %v", err)}
	}
	defer wroot.Close()
	if err := ensureInputsDir(wroot); err != nil {
		return &Error{Reason: "输入区基目录不可用: " + err.Error()}
	}
	if err := recoverStaging(wroot, taskID); err != nil {
		return err
	}
	// 暂存目录：inputs 内（rename 同文件系统）、点前缀隔离命名空间；随机 token
	// 防撞，撞名重试换 token。
	var stageRel string
	for i := 0; i < 3; i++ {
		candidate := inputsRel + "/" + stagingPrefix + taskID + "-" + randToken()
		err := wroot.Mkdir(candidate, 0o755)
		if err == nil {
			stageRel = candidate
			break
		}
		if !errors.Is(err, fs.ErrExist) || i == 2 {
			return &Error{Reason: fmt.Sprintf("创建暂存目录失败: %v", err)}
		}
	}
	if stageRel == "" {
		return &Error{Reason: "创建暂存目录失败: 随机名连续冲突"}
	}
	// 阶段一：全量暂存复制+校验（尚未触碰输入区既有内容，失败只需清理暂存）
	plans := make([]mountPlan, 0, len(ups))
	for _, up := range ups {
		if err := ctx.Err(); err != nil {
			_ = removeAllRel(wroot, stageRel)
			return timeoutErr(err)
		}
		if err := stageUpstream(ctx, wroot, stageRel, up); err != nil {
			_ = removeAllRel(wroot, stageRel)
			return err
		}
		plans = append(plans, mountPlan{
			up:       up,
			srcRel:   stageRel + "/" + up.TaskID,
			dstRel:   inputsRel + "/" + up.TaskID,
			trashRel: stageRel + "/trash/" + up.TaskID,
		})
	}
	if err := rootMkdirAll(wroot, stageRel+"/trash"); err != nil {
		_ = removeAllRel(wroot, stageRel)
		return &Error{Reason: fmt.Sprintf("创建发布暂存失败: %v", err)}
	}
	// 阶段二：发布事务。journal 先行落盘（发布意图，pending 全量），逐上游推进。
	j := &journal{TaskID: taskID, Steps: make([]journalStep, len(plans))}
	for i, p := range plans {
		j.Steps[i] = journalStep{Upstream: p.up.TaskID, State: stepPending}
	}
	if err := writeJournal(wroot, stageRel, j); err != nil {
		_ = removeAllRel(wroot, stageRel)
		return &Error{Reason: fmt.Sprintf("写发布事务日志失败: %v", err)}
	}
	type moved struct{ from, to string } // dst→trash 移开记录（回滚归位基准）
	var published []string               // 已发布 dstRel（回滚撤销基准）
	var aside []moved
	fail := func(e *Error) error {
		var probs []string
		for _, dst := range published { // 撤销已发布：删新目录
			if err := removeAllRel(wroot, dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
				probs = append(probs, fmt.Sprintf("撤销已发布目录 %s 失败: %v", dst, err))
			}
		}
		for i := len(aside) - 1; i >= 0; i-- { // 旧目录归位（含已发布与发布失败项）
			if err := renameDir(absOf(wroot, aside[i].from), absOf(wroot, aside[i].to)); err != nil {
				probs = append(probs, fmt.Sprintf("上游 %s 旧目录归位失败（旧目录材料保留于 %s，需人工恢复）: %v",
					filepath.Base(aside[i].to), absOf(wroot, aside[i].from), err))
			}
		}
		if len(probs) > 0 {
			// 回滚未完全：保留暂存/trash/journal 全部材料（重挂时 recoverStaging
			// 按实物收敛或人工恢复），聚合报告原始失败+回滚问题。
			probs = append(probs, "回滚未完全，暂存与 trash 材料已保留于 "+absOf(wroot, stageRel))
			return &Error{IDem: e.IDem, Upstream: e.Upstream, Path: e.Path,
				Reason: e.Reason + "；" + strings.Join(probs, "；")}
		}
		_ = removeAllRel(wroot, stageRel) // 回滚完成：暂存（含 trash/journal）一并清理
		_ = syncDirRel(wroot, inputsRel)
		return e
	}
	for i := range plans {
		p := plans[i]
		if err := ctx.Err(); err != nil {
			return fail(timeoutErr(err))
		}
		// rename 前重验路径组件（每步重验，压缩校验→rename 窗口；os.Rename 目标
		// 端不跟随符号链接，源端组件由本重验把关）。
		if err := verifyDirRel(wroot, inputsRel, "输入区基目录"); err != nil {
			return fail(&Error{Upstream: p.up.TaskID, Reason: "发布前输入区校验失败: " + err.Error()})
		}
		if err := verifyDirRel(wroot, stageRel, "暂存目录"); err != nil {
			return fail(&Error{Upstream: p.up.TaskID, Reason: "发布前暂存校验失败: " + err.Error()})
		}
		if _, err := wroot.Lstat(p.dstRel); err == nil {
			// 同上游目录已存在（恢复重挂/同 agent 后续任务同依赖重挂）：内容为
			// 同一上游登记产物的确定性复制，移开替换即幂等重发布。挂载发生在
			// 下游 agent 的 workdir 互斥持有期内（调用方保证），无受管并发读者。
			if err := renameDir(absOf(wroot, p.dstRel), absOf(wroot, p.trashRel)); err != nil {
				return fail(&Error{Upstream: p.up.TaskID, Reason: fmt.Sprintf("移开旧输入目录失败: %v", err)})
			}
			aside = append(aside, moved{from: p.trashRel, to: p.dstRel})
			if err := syncDirRel(wroot, inputsRel); err != nil {
				return fail(&Error{Upstream: p.up.TaskID, Reason: fmt.Sprintf("移开旧目录 fsync 失败: %v", err)})
			}
			if err := syncDirRel(wroot, stageRel); err != nil {
				return fail(&Error{Upstream: p.up.TaskID, Reason: fmt.Sprintf("移开旧目录 fsync 失败: %v", err)})
			}
			j.Steps[i].State = stepOldMoved
			if err := writeJournal(wroot, stageRel, j); err != nil {
				return fail(&Error{Upstream: p.up.TaskID, Reason: fmt.Sprintf("推进发布事务日志失败: %v", err)})
			}
		}
		if err := renameDir(absOf(wroot, p.srcRel), absOf(wroot, p.dstRel)); err != nil {
			return fail(&Error{Upstream: p.up.TaskID, Reason: fmt.Sprintf("发布输入目录失败: %v", err)})
		}
		if err := syncDirRel(wroot, inputsRel); err != nil {
			return fail(&Error{Upstream: p.up.TaskID, Reason: fmt.Sprintf("发布 fsync 失败: %v", err)})
		}
		j.Steps[i].State = stepPublished
		if err := writeJournal(wroot, stageRel, j); err != nil {
			return fail(&Error{Upstream: p.up.TaskID, Reason: fmt.Sprintf("推进发布事务日志失败: %v", err)})
		}
		published = append(published, p.dstRel)
	}
	// 末次发布后复查超时/取消：输入区不得视为就绪（调用方不得 MarkStarted），
	// 按失败语义回滚到挂载前原状。
	if err := ctx.Err(); err != nil {
		return fail(timeoutErr(err))
	}
	if err := removeAllRel(wroot, stageRel); err != nil {
		// 发布已全部完成，仅清理失败：残留点前缀目录，下次挂载 recoverStaging
		// 自愈（journal 全 published → 直接清理）。如实报错不静默。
		return &Error{Reason: fmt.Sprintf("发布完成但清理暂存失败（残留 %s，重挂自愈）: %v",
			absOf(wroot, stageRel), err)}
	}
	if err := syncDirRel(wroot, inputsRel); err != nil {
		return &Error{Reason: fmt.Sprintf("发布完成但输入区 fsync 失败: %v", err)}
	}
	return nil
}

// ---------- 暂存复制（ctx 贯穿 + 字节上限 + fd 级校验） ----------

// stageUpstream 单个上游的暂存复制+校验：清单件数先过限（对登记重检，防御超
// 清单），逐文件纯复制后复读 SHA-256 对照登记。内容源二态：本机上游产物根
//（up.Workdir，配置管理路径与 runner exec 同源可信）以 OpenSourceFile 打开源文
// 件（源侧定界：产物路径不可能逃出上游 workdir）；远端上游经 Fetch 字节源取回
//（SHA/大小校验在取回链完成，见 UpstreamInputs.Fetch）。
func stageUpstream(ctx context.Context, wroot *os.Root, stageRel string, up *UpstreamInputs) error {
	if len(up.Artifacts) > protocol.MaxArtifacts {
		return &Error{Upstream: up.TaskID,
			Reason: fmt.Sprintf("产物件数超上限（登记 %d 件 > %d）", len(up.Artifacts), protocol.MaxArtifacts)}
	}
	upStageRel := stageRel + "/" + up.TaskID // 暂存内容按上游 ID 分层（发布 rename 单位）
	for _, art := range up.Artifacts {
		if err := ctx.Err(); err != nil {
			return timeoutErr(err)
		}
		if err := stageOne(ctx, wroot, upStageRel, up, art); err != nil {
			return err
		}
	}
	return nil
}

// stageOne 复制单个产物文件到暂存区并校验：本机源经 OpenSourceFile 打开（整条
// 路径内核强制不跟随+不阻塞，见 sourceopen_*.go；打开后 fd fstat 确认常规文
// 件），远端源经 Fetch 取回（已按登记校验）；复制以登记大小+1 为字节上限，复
// 制后核对实得字节数==登记大小（防源检查后增长/缩短）；复读盘上暂存件核对尺
// 寸与 SHA-256 对照登记。
func stageOne(ctx context.Context, wroot *os.Root, stageRel string, up *UpstreamInputs, art protocol.Artifact) error {
	e := func(reason string) *Error { return &Error{Upstream: up.TaskID, Path: art.Path, Reason: reason} }
	timeout := func() *Error {
		te := timeoutErr(ctx.Err())
		te.Upstream, te.Path = up.TaskID, art.Path
		return te
	}
	if art.Size < 0 {
		return e("登记大小非法（负值）")
	}
	if art.Size > protocol.ArtifactLimit {
		return e(fmt.Sprintf("单件超上限（登记 %d 字节 > %d）", art.Size, protocol.ArtifactLimit))
	}
	rel, err := mountRelPath(up.TaskID, art.Path)
	if err != nil {
		return err
	}
	var src io.Reader
	var srcPerm fs.FileMode = 0o644 // 远端字节流无源 mode：固定 0644（内容完整性由 SHA 校验承担）
	switch {
	case up.Fetch != nil: // 远端字节流：取回链已校验 SHA/大小（失败返回 *Error）
		data, ferr := up.Fetch(ctx, art)
		if ferr != nil {
			return ferr
		}
		src = bytes.NewReader(data)
	default: // 本机磁盘源：内核强制不跟随+不阻塞的 rooted 打开 + fd fstat 校验
		f, sfi, err := OpenSourceFile(up.Workdir, "outputs/"+up.TaskID+"/"+rel)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return e("上游产物缺失或不可读（上游 completed ≠ 目录仍在，保留期语义见 SPEC 4.12）")
			}
			return e(fmt.Sprintf("上游产物不可读: %v", err))
		}
		defer f.Close()
		if sfi.Size() != art.Size {
			return e(fmt.Sprintf("与登记大小不一致（登记 %d，实得 %d）", art.Size, sfi.Size()))
		}
		src, srcPerm = f, sfi.Mode().Perm()
	}
	dstRel := stageRel + "/" + rel
	if err := rootMkdirAll(wroot, path.Dir(dstRel)); err != nil {
		return e(fmt.Sprintf("创建暂存子目录失败: %v", err))
	}
	out, err := wroot.OpenFile(dstRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, srcPerm)
	if err != nil {
		return e(fmt.Sprintf("创建暂存件失败: %v", err))
	}
	// 纯复制（O_EXCL 独占创建）：登记大小+1 为字节上限——实得超出即源在检查后
	// 增长，拒绝。
	n, cerr := copyStream(ctx, out, io.LimitReader(src, art.Size+1))
	if cerr == nil {
		cerr = out.Sync()
	}
	if cerr2 := out.Close(); cerr == nil {
		cerr = cerr2
	}
	if cerr != nil {
		if isCtxErr(cerr) {
			return timeout()
		}
		return e(fmt.Sprintf("复制失败（纯复制，不硬链接）: %v", cerr))
	}
	if n > art.Size {
		return e(fmt.Sprintf("实得字节超过登记大小（源在检查后增长：登记 %d，实得 %d）", art.Size, n))
	}
	if n != art.Size {
		return e(fmt.Sprintf("与登记大小不一致（登记 %d，实得 %d）", art.Size, n))
	}
	// 复读盘上暂存件：尺寸与 SHA-256 对照登记（校验对象=盘上实际内容）。
	sum, n2, err := hashFile(ctx, wroot, dstRel)
	if err != nil {
		if isCtxErr(err) {
			return timeout()
		}
		return e(fmt.Sprintf("复制件校验读取失败: %v", err))
	}
	if n2 != art.Size {
		return e(fmt.Sprintf("盘上暂存件与登记大小不一致（登记 %d，实得 %d）", art.Size, n2))
	}
	if sum != strings.ToLower(art.Sha256) {
		return e(fmt.Sprintf("SHA-256 与登记不一致（登记 %s，实得 %s）", art.Sha256, sum))
	}
	return nil
}

// OpenSourceFile 打开产物源文件并核验为常规文件（源读取统一安全口径：Local 挂
// 载与 service/Remote 源读取共用同一实现，2026-10-24 复审 R1）。整条路径由内核
// 在 open 原语上强制不跟随（平台实现见 sourceopen_*.go：Unix 为逐段 openat 游
// 走，O_DIRECTORY|O_NOFOLLOW 中间段 + O_NOFOLLOW|O_NONBLOCK 末段——每段解析锚
// 定上一段目录 fd，检查即打开、无 Lstat→Open 换入链接/FIFO 的窗口；Windows 为
// 逐段 NtCreateFile 游走，每段 OBJ_DONT_REPARSE 打开时约束 reparse/link 解析
// ——os.Root 在 Windows 遇 reparse point 会读链跟随根内链接，不能承担该契约，
// 见 sourceopen_windows.go；Windows 无 FIFO open 阻塞语义，无需 O_NONBLOCK 对
// 应物）。打开后 fd fstat 确认 regular file——校验的对象与后续读取的对象是同
// 一 fd；越界路径（绝对/..）入口即拒。纵深说明：游走每段相对锚定已打开目录句
// 柄，根外不可达，最坏情形只是打开根内另一常规文件，其内容过不了调用方对照登
// 记的 SHA-256 终检，读不出登记外内容。
func OpenSourceFile(root, rel string) (*os.File, fs.FileInfo, error) {
	p := path.Clean(filepath.ToSlash(rel))
	if p == "." || path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
		return nil, nil, errors.New("源路径越界（拒绝）")
	}
	f, fi, err := openSourceFile(root, p)
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, errors.New("非常规文件（符号链接/特殊文件，拒绝）")
	}
	return f, fi, nil
}

// copyStream ctx 感知分块复制：每块前检查取消/超时（同机本地文件读不长阻塞，
// 块间检查足以收敛独立超时与中止）。
func copyStream(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			wn, werr := dst.Write(buf[:n])
			total += int64(wn)
			if werr != nil {
				return total, werr
			}
			if wn < n {
				return total, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return total, nil
			}
			return total, rerr
		}
	}
}

// hashFile 流式计算 inputs 域内相对路径文件的内容 SHA-256 与字节数（ctx 感知）。
func hashFile(ctx context.Context, wroot *os.Root, rel string) (string, int64, error) {
	f, err := wroot.Open(rel)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := copyStream(ctx, h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// mountRelPath 产物路径 → 挂载相对路径：剥 outputs/<上游task_id>/ 前缀（挂载
// 目录已按上游 ID 分层），拒绝口径外与穿越路径（防清单被污染越出输入区）。
func mountRelPath(upTaskID, artPath string) (string, error) {
	p := path.Clean(filepath.ToSlash(artPath))
	prefix := "outputs/" + upTaskID + "/"
	if !strings.HasPrefix(p, prefix) || p == prefix {
		return "", &Error{Upstream: upTaskID, Path: artPath,
			Reason: "产物路径不在上游 outputs/<task_id>/ 口径内"}
	}
	rel := strings.TrimPrefix(p, prefix)
	if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", &Error{Upstream: upTaskID, Path: artPath, Reason: "产物相对路径不合法（防穿越校验）"}
	}
	return rel, nil
}

// timeoutErr 独立超时/中止的失败包装（准备阶段独立于任务执行超时，FILE-COLLAB L2）。
func timeoutErr(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Reason: "输入准备超时（独立于任务执行超时）"}
	}
	return &Error{Reason: "输入准备被中止"}
}

// isCtxErr 取消/超时类错误判定（复制/校验路径区分 ctx 中断与真实 IO 失败）。
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// randToken 6 字节加密随机 hex（暂存目录名防撞）。
func randToken() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("inputs: crypto/rand 读取失败: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
