// 审查/证据 schema 与循环事实记录（W05-01 交付②）。
//
// 硬门槛（W05 任务书「写审与讨论的硬门槛」）逐条落点：
//   - revision 至少含基线、内容/补丁 SHA-256、编写轮次、依赖产物引用与测试执行
//     证据 → Revision 字段齐备；
//   - issue 至少含稳定 ID、级别、文件:行、发现版本、回复/修复次数与复审结果 →
//     Issue 字段齐备；
//   - 只收到 agent 自报测试通过不得给已证实 → EvidenceSource.CanVerify：自报
//     永不可证实（记录面拒绝 + 判定面再验，双层把守），适配器输出 unverified
//     保持未知；
//   - CLI 退出0/协议解析成功/轮完成/工作验收四层分离 → 四个独立证据字段；
//   - 代码改变使旧 review 失效 → RegisterRevision 新版本触发失效投影 + 失效
//     留痕（TrailReviewInvalidated；原复审记录按序保留不抹改）。
//
// 记录方法（Register*/Record*）是循环事实写入面，供 W05-02/03 装配层调用，成功
// 变更后经 Config.OnPersist 通知装配层落盘（W09-A 接线，persist.go 单源）；写入
// 方法均为「公开壳 + 锁内实现」双层——通知在写锁释放后由壳统一触发（写锁内同步
// 回调 = 死锁面）。StrategyHooks 三方法（strategy.go）只读这些事实做纯判定，绝无
// 副作用——写入面与判定面严格分离。
//
// 【问题重开语义定案（W05-01 终审必修1，供 RFC 引用）】审查问题以稳定 ID 归档
// 修复闭环；同 key（提出者/文件:行/标题）重报一条**已确认修复**的问题即回归重
// 开（regression）——重报事实进入有效状态投影（IssueEffectiveState）：最近复审
// 之后存在同 key 重报 → 有效状态回 open，重新阻塞返工触发（DecidePhaseAdvance）
// 与完成门（EvaluateCompletion 的必须修清零）。重开问题须对**当前版本**做新一轮
// 复审（confirmed_fixed 且 against 当前版本 hash）才再次确认。重报不改 ID
// （Occurrences 计数），留痕区分 issue_reopened（对已确认问题的重报）与
// issue_rereported（对未解问题的重报）。先后判定用问题事件序号
// （Issue.EventSeq/LastReportedSeq/ReReviewRecord.Seq）承担，不依赖时钟同刻序。
package strategy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ---------- 证据分层（四层分离） ----------

// EvidenceLayer 测试执行证据四层（硬门槛：四类分离字段，逐层独立判定，不合并）。
type EvidenceLayer string

const (
	LayerCLIExit    EvidenceLayer = "cli_exit"       // 第一层：测试命令真实执行且退出 0
	LayerProtocol   EvidenceLayer = "protocol_parse" // 第二层：执行输出按协议解析成功
	LayerRoundDone  EvidenceLayer = "round_complete" // 第三层：编写/测试轮完成（轮收敛落账）
	LayerWorkAccept EvidenceLayer = "work_accept"    // 第四层：工作验收（有权人工核对/服务端验收事实）
)

// Valid 取值校验。
func (l EvidenceLayer) Valid() bool {
	switch l {
	case LayerCLIExit, LayerProtocol, LayerRoundDone, LayerWorkAccept:
		return true
	}
	return false
}

// EvidenceSource 证据来源分层标记（「只收到 agent 自报测试通过不得给已证实」的
// 落点：来源分层 + CanVerify 判定）。
type EvidenceSource string

const (
	SourceSelfReport      EvidenceSource = "agent_self_report"  // agent 自报（永不可证实）
	SourceExecutorReceipt EvidenceSource = "executor_receipt"   // CLI 执行回执（退出码/耗时/命令 hash）
	SourceProtocolReceipt EvidenceSource = "protocol_receipt"   // 协议解析回执（结构化结果）
	SourceHostObservation EvidenceSource = "host_observation"   // 服务端事实观察（轮收敛/产物 hash 对账）
	SourceHumanConfirm    EvidenceSource = "human_confirmation" // 有权人工核对（主体记入 Ref）
)

// Valid 取值校验。
func (s EvidenceSource) Valid() bool {
	switch s {
	case SourceSelfReport, SourceExecutorReceipt, SourceProtocolReceipt, SourceHostObservation, SourceHumanConfirm:
		return true
	}
	return false
}

// CanVerify 该来源能否承担「已证实」：agent 自报永不可证实——必须额外有明确
// 证据（执行/协议回执、服务端事实）或有权人工核对才能完成（硬门槛原文）。
func (s EvidenceSource) CanVerify() bool { return s != SourceSelfReport }

// Evidence 单层证据（来源分层标记必填；Ref 为回执号/命令 hash/人工主体等定位）。
type Evidence struct {
	Verified bool           `json:"verified"`
	Source   EvidenceSource `json:"source"`
	Ref      string         `json:"ref"`
	At       time.Time      `json:"at,omitempty"`
}

// EvidenceVerified 证据已证实判定：非空 + 标记证实 + 来源可证实。自报来源即使
// 被误标 Verified=true 也判未证实（判定面再验，防记录面绕过）。
func EvidenceVerified(ev *Evidence) bool {
	return ev != nil && ev.Verified && ev.Source.Valid() && ev.Source.CanVerify()
}

// ---------- 版本（Revision） ----------

// ArtifactRef 依赖产物引用（登记范围/大小限制继承 W04 产物协议；中继不等于
// 仓库自动同步——超限阻塞说明，由装配层处理）。
type ArtifactRef struct {
	Kind   string `json:"kind"`             // artifact|file|url（W04 产物协议词表）
	Ref    string `json:"ref"`              // 产物 ID / 路径
	SHA256 string `json:"sha256,omitempty"` // 产物内容 hash（登记 hash 必验格式）
	Note   string `json:"note,omitempty"`
}

// Revision 固定版本记录（跨机审查须明确代码基线、提交/补丁及哈希——PLAN §6）。
type Revision struct {
	// ID 稳定版本 ID（rev- + 内容/补丁 hash 派生 hex）：同内容同补丁 = 同版本
	// （重复登记幂等，不产生新版本）。
	ID string `json:"id"`
	// Baseline 代码基线（commit / 工作区锚）。
	Baseline string `json:"baseline"`
	// ContentSHA256 内容 SHA-256（64 位 hex；版本判等唯一依据）。
	ContentSHA256 string `json:"content_sha256"`
	// PatchSHA256 补丁 SHA-256（可空：整树交付无补丁形态）。
	PatchSHA256 string `json:"patch_sha256,omitempty"`
	// AuthorRound 编写轮次。
	AuthorRound int64 `json:"author_round"`
	// Author 编写成员。
	Author string `json:"author"`
	// Dependencies 依赖产物引用。
	Dependencies []ArtifactRef `json:"dependencies,omitempty"`
	// RegisteredAt 登记时刻（UTC）。
	RegisteredAt time.Time `json:"registered_at"`
	// 测试执行证据四类分离字段（硬门槛）。
	TestCLI    *Evidence `json:"test_cli,omitempty"`      // CLI 退出 0
	TestProto  *Evidence `json:"test_protocol,omitempty"` // 协议解析成功
	TestRound  *Evidence `json:"test_round,omitempty"`    // 轮完成
	TestAccept *Evidence `json:"test_accept,omitempty"`   // 工作验收
}

// RevisionInput 版本登记入参。
type RevisionInput struct {
	Baseline      string
	ContentSHA256 string
	PatchSHA256   string
	AuthorRound   int64
	Author        string
	Dependencies  []ArtifactRef
}

// ---------- 问题（Issue） ----------

// Severity 问题级别（必须修/建议修/可不修——完成判据只被 must 阻塞）。
type Severity string

const (
	SeverityMust   Severity = "must"   // 必须修
	SeverityShould Severity = "should" // 建议修
	SeverityInfo   Severity = "info"   // 可不修
)

// Valid 取值校验。
func (s Severity) Valid() bool {
	switch s {
	case SeverityMust, SeverityShould, SeverityInfo:
		return true
	}
	return false
}

// Blocking 该级别是否阻塞阶段推进（must/should 阻塞返工触发；info 不阻塞）。
func (s Severity) Blocking() bool { return s == SeverityMust || s == SeverityShould }

// ReReviewStatus 复审结论原始值。
type ReReviewStatus string

const (
	ReReviewConfirmed ReReviewStatus = "confirmed_fixed" // 复审确认已修
	ReReviewStillOpen ReReviewStatus = "still_open"      // 复审仍未解
)

// Valid 可记录的复审结论（invalidated 不是可记录值——它是版本变更的投影态，
// 由 IssueEffectiveState 推导 + 失效留痕承担，防人工直写抹掉原结论）。
func (r ReReviewStatus) Valid() bool {
	switch r {
	case ReReviewConfirmed, ReReviewStillOpen:
		return true
	}
	return false
}

// IssueState 问题有效状态（原始复审记录 + 版本匹配的投影；不另存状态字段——
// 单一事实源）。
type IssueState string

const (
	IssueOpen           IssueState = "open"            // 尚无复审结论
	IssueConfirmedFixed IssueState = "confirmed_fixed" // 复审确认已修（对当前版本）
	IssueStillOpen      IssueState = "still_open"      // 复审仍未解
	IssueStale          IssueState = "stale"           // 旧复审结论因版本变更失效（留痕待复审）
)

// FixAttempt 修复声明（编写线程回应问题；Result 由复审回填）。
type FixAttempt struct {
	At          time.Time      `json:"at"`
	Round       int64          `json:"round"`        // 返工轮次
	VersionHash string         `json:"version_hash"` // 修复声明时的版本（新版本 hash）
	Note        string         `json:"note,omitempty"`
	Result      ReReviewStatus `json:"result"` // pending 复核中（空值）；复审后回填
	// VerifiedAgainst 复核通过所针对的版本 hash（确认后若版本再变即失效投影）。
	VerifiedAgainst string `json:"verified_against,omitempty"`
}

// ReReviewRecord 复审记录（按序保留，永不抹改——失效留痕=记录保留 + 失效投影 +
// Trail 事件）。
type ReReviewRecord struct {
	// Seq 事件序号（登记时的 Issue.EventSeq；与 LastReportedSeq 比较判重开投影）。
	Seq         int64          `json:"seq"`
	At          time.Time      `json:"at"`
	Reviewer    string         `json:"reviewer"`
	AgainstHash string         `json:"against_hash"` // 复审针对版本
	Result      ReReviewStatus `json:"result"`
	Note        string         `json:"note,omitempty"`
}

// Issue 审查问题记录。
type Issue struct {
	// ID 稳定问题 ID（iss- + (reviewer,file,line,标题) 派生 hex）：同 key 重报
	// 不改 ID（Occurrences 计数），跨复审轮次稳定（AC13 两 ID 稳定之一）。
	ID string `json:"id"`
	// Severity 级别（must/should/info）。
	Severity Severity `json:"severity"`
	// File / Line 文件:行（String() 拼装展示）。
	File string `json:"file"`
	Line int    `json:"line"`
	// Title 问题标题（归一化空白后参与 ID 派生）。
	Title string `json:"title"`
	// Reviewer 提出者（审查成员）。
	Reviewer string `json:"reviewer"`
	// FoundInVersion 发现版本（内容 hash）；FoundInRound 发现时编写轮次。
	FoundInVersion string `json:"found_in_version"`
	FoundInRound   int64  `json:"found_in_round"`
	// CreatedAt / UpdatedAt 时刻（UTC）。
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Occurrences 同 key 重报次数（≥1）。
	Occurrences int `json:"occurrences"`
	// Replies 回复次数（编写人回应往返）。
	Replies int `json:"replies"`
	// EventSeq 问题事件单调序号（登记/重报/复审各 +1；重开投影与恢复完整性
	// 校验用）。
	EventSeq int64 `json:"event_seq"`
	// LastReportedSeq 最近一次同 key 重报的事件序号：大于最近复审记录 Seq 即
	// 回归重开投影（见 IssueEffectiveState——重报已确认问题即回归）。
	LastReportedSeq int64 `json:"last_reported_seq"`
	// Fixes 修复声明（次数 = len；逐次带复审回填结论）。
	Fixes []FixAttempt `json:"fixes,omitempty"`
	// ReReviews 复审记录（按序保留）。
	ReReviews []ReReviewRecord `json:"re_reviews,omitempty"`
	// Note 提出时备注。
	Note string `json:"note,omitempty"`
}

// String 文件:行 展示（硬门槛必需字段的人话形态）。
func (is *Issue) String() string { return fmt.Sprintf("%s:%d", is.File, is.Line) }

// IssueInput 问题登记入参。
type IssueInput struct {
	Reviewer     string
	Severity     Severity
	File         string
	Line         int
	Title        string
	FoundInHash  string // 发现版本（内容 hash）
	FoundInRound int64
	Note         string
}

// ReReviewInput 复审登记入参（Result 仅 confirmed_fixed|still_open 可记录）。
type ReReviewInput struct {
	Reviewer    string
	AgainstHash string
	Result      ReReviewStatus
	Note        string
}

// 哈希格式校验（内容/补丁/产物 hash 一律 SHA-256 hex 64——「证据绑定真实执行
// 回执/hash」的格式把守）。
func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// reviewError 构造记录面参数错误。
func revErrf(sentinel *Error, format string, args ...any) error {
	return errf(sentinel, format, args...)
}

// 审查记录错误哨兵。
var (
	// ErrRevisionInvalid 版本登记参数非法（缺基线/hash 格式错等）。
	ErrRevisionInvalid = &Error{Code: "revision_invalid", msg: "版本登记参数非法"}
	// ErrRevisionNotFound 版本不存在（证据/复审锚定的 hash 无登记版本）。
	ErrRevisionNotFound = &Error{Code: "revision_not_found", msg: "版本不存在"}
	// ErrEvidenceInvalid 证据登记非法（来源未知/自报标证实）。
	ErrEvidenceInvalid = &Error{Code: "evidence_invalid", msg: "证据登记非法"}
	// ErrIssueInvalid 问题登记参数非法。
	ErrIssueInvalid = &Error{Code: "issue_invalid", msg: "问题登记参数非法"}
	// ErrIssueNotFound 问题不存在。
	ErrIssueNotFound = &Error{Code: "issue_not_found", msg: "问题不存在"}
	// ErrReReviewInvalid 复审登记非法。
	ErrReReviewInvalid = &Error{Code: "rereview_invalid", msg: "复审登记非法"}
	// ErrFixInvalid 修复声明非法。
	ErrFixInvalid = &Error{Code: "fix_invalid", msg: "修复声明非法"}
)

// issueID 稳定问题 ID：同（提出者,文件:行,标题）→ 同 ID（跨复审轮稳定）。
func issueID(reviewer, file string, line int, title string) string {
	sum := sha256.Sum256([]byte("issue\x00" + reviewer + "\x00" + file + "\x00" +
		fmt.Sprint(line) + "\x00" + normalizeTitle(title)))
	return "iss-" + hex.EncodeToString(sum[:])[:12]
}

// normalizeTitle 标题归一化（空白折叠后参与 ID 派生——同义标题重报同 ID）。
func normalizeTitle(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// revisionID 稳定版本 ID：内容+补丁 hash 派生（同内容同补丁 = 同版本）。
func revisionID(contentHash, patchHash string) string {
	sum := sha256.Sum256([]byte("rev\x00" + contentHash + "\x00" + patchHash))
	return "rev-" + hex.EncodeToString(sum[:])[:12]
}

// RegisterRevision 登记固定版本（编写线程产出）。幂等：同内容同补丁 = 同版本，
// 命中既有记录原样返回（created=false，不触发失效）。新版本（hash ≠ 当前版本）
// 登记 → 「代码改变使旧 review 失效」：对旧版本做出的复审确认自动进入失效投影
// （IssueEffectiveState → stale），并落失效留痕事件（原复审记录保留不抹改）。
// 公开壳：锁内实现完整返回（写锁已释放）后按实际登记（created）触发持久化通知
// ——幂等命中零变更不通知。
func (e *Engine) RegisterRevision(spaceID string, in RevisionInput) (Revision, bool, error) {
	rev, created, err := e.registerRevision(spaceID, in)
	if err == nil && created {
		e.notifyPersist()
	}
	return rev, created, err
}

// registerRevision RegisterRevision 的锁内实现。
func (e *Engine) registerRevision(spaceID string, in RevisionInput) (Revision, bool, error) {
	if strings.TrimSpace(in.Baseline) == "" {
		return Revision{}, false, revErrf(ErrRevisionInvalid, "基线必填")
	}
	if !validHash(in.ContentSHA256) {
		return Revision{}, false, revErrf(ErrRevisionInvalid, "content_sha256 须为 SHA-256 hex64，得 %q", in.ContentSHA256)
	}
	if in.PatchSHA256 != "" && !validHash(in.PatchSHA256) {
		return Revision{}, false, revErrf(ErrRevisionInvalid, "patch_sha256 须为 SHA-256 hex64 或空")
	}
	if strings.TrimSpace(in.Author) == "" {
		return Revision{}, false, revErrf(ErrRevisionInvalid, "编写成员必填")
	}
	if in.AuthorRound < 0 {
		return Revision{}, false, revErrf(ErrRevisionInvalid, "编写轮次为负")
	}
	deps := make([]ArtifactRef, 0, len(in.Dependencies))
	for i, d := range in.Dependencies {
		if strings.TrimSpace(d.Ref) == "" || strings.TrimSpace(d.Kind) == "" {
			return Revision{}, false, revErrf(ErrRevisionInvalid, "依赖产物 %d 缺 kind/ref", i)
		}
		if d.SHA256 != "" && !validHash(d.SHA256) {
			return Revision{}, false, revErrf(ErrRevisionInvalid, "依赖产物 %d sha256 格式错", i)
		}
		deps = append(deps, d)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	id := revisionID(in.ContentSHA256, in.PatchSHA256)
	for i := range lp.Revisions {
		if lp.Revisions[i].ID == id {
			return cloneRevision(&lp.Revisions[i]), false, nil
		}
	}
	prevHash := e.currentHashLocked(spaceID)
	rev := Revision{
		ID:            id,
		Baseline:      strings.TrimSpace(in.Baseline),
		ContentSHA256: in.ContentSHA256,
		PatchSHA256:   in.PatchSHA256,
		AuthorRound:   in.AuthorRound,
		Author:        strings.TrimSpace(in.Author),
		Dependencies:  deps,
		RegisteredAt:  e.now(),
	}
	lp.Revisions = append(lp.Revisions, rev)
	e.appendTrail(lp, TrailRevisionRegistered,
		fmt.Sprintf("%s 内容 %s… 轮 %d（前一版本 %s…）", id, in.ContentSHA256[:12], in.AuthorRound, tail(prevHash, 12)))
	if prevHash != "" && prevHash != in.ContentSHA256 {
		e.invalidateReviews(lp, prevHash, in.ContentSHA256)
	}
	return cloneRevision(&rev), true, nil
}

// invalidateReviews 版本变更失效留痕：对旧版本做出的复审确认整体失效（投影为
// stale；原记录保留），trail 记录旧/新版本与受影响问题清单。
func (e *Engine) invalidateReviews(lp *SpaceLoop, oldHash, newHash string) {
	var affected []string
	for i := range lp.Issues {
		st := IssueEffectiveState(&lp.Issues[i], newHash)
		if st == IssueStale {
			affected = append(affected, lp.Issues[i].ID)
		}
	}
	detail := fmt.Sprintf("版本变更 %s… → %s…：旧复审结论失效", tail(oldHash, 12), tail(newHash, 12))
	if len(affected) > 0 {
		detail += fmt.Sprintf("（受影响 %d 条：%s）", len(affected), strings.Join(affected, ","))
	}
	e.appendTrail(lp, TrailReviewInvalidated, detail)
}

// RecordEvidence 登记测试执行证据（绑定到指定版本 hash——证据绑定真实执行回执/
// hash）。自报来源标记 Verified=true 拒绝（记录面第一道把守；判定面
// EvidenceVerified 第二道）。公开壳：锁内实现完整返回（写锁已释放）后按实际变更
// 触发持久化通知——校验/锚定拒绝不通知。
func (e *Engine) RecordEvidence(spaceID, versionHash string, layer EvidenceLayer, ev Evidence) error {
	if err := e.recordEvidence(spaceID, versionHash, layer, ev); err != nil {
		return err
	}
	e.notifyPersist()
	return nil
}

// recordEvidence RecordEvidence 的锁内实现。
func (e *Engine) recordEvidence(spaceID, versionHash string, layer EvidenceLayer, ev Evidence) error {
	if !layer.Valid() {
		return revErrf(ErrEvidenceInvalid, "未知证据层 %q", string(layer))
	}
	if !ev.Source.Valid() {
		return revErrf(ErrEvidenceInvalid, "未知证据来源 %q", string(ev.Source))
	}
	if ev.Verified && !ev.Source.CanVerify() {
		return revErrf(ErrEvidenceInvalid, "agent 自报不得标记已证实（须回执/服务端事实/有权人工核对）")
	}
	if strings.TrimSpace(ev.Ref) == "" {
		return revErrf(ErrEvidenceInvalid, "证据定位 Ref 必填（回执号/hash/人工主体）")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	rev := e.revisionByHashLocked(lp, versionHash)
	if rev == nil {
		return revErrf(ErrRevisionNotFound, "版本 %s… 未登记", tail(versionHash, 12))
	}
	slot := rev.slotFor(layer)
	if *slot != nil && EvidenceVerified(*slot) && !EvidenceVerified(&ev) {
		// 已证实层不降级：事实「曾证实」不因后到自报/未证实记录消失（覆盖仅
		// 允许证据升级或同级更新）。
		return revErrf(ErrEvidenceInvalid, "层 %s 已有已证实证据（来源 %s），不得以未证实记录覆盖",
			string(layer), string((*slot).Source))
	}
	cp := ev
	cp.At = e.now()
	*slot = &cp
	e.appendTrail(lp, TrailEvidenceRecorded,
		fmt.Sprintf("%s… 层 %s 来源 %s 证实=%v（%s）", tail(versionHash, 12), string(layer), string(ev.Source), ev.Verified, ev.Ref))
	return nil
}

// slotFor 证据层 → 分离字段槽位（四层分离的物理落点）。
func (r *Revision) slotFor(layer EvidenceLayer) **Evidence {
	switch layer {
	case LayerCLIExit:
		return &r.TestCLI
	case LayerProtocol:
		return &r.TestProto
	case LayerRoundDone:
		return &r.TestRound
	case LayerWorkAccept:
		return &r.TestAccept
	}
	return nil
}

// revisionByHashLocked 按内容 hash 查版本（调用方持锁）。
func (e *Engine) revisionByHashLocked(lp *SpaceLoop, hash string) *Revision {
	for i := range lp.Revisions {
		if lp.Revisions[i].ContentSHA256 == hash {
			return &lp.Revisions[i]
		}
	}
	return nil
}

// RecordIssue 登记审查问题（针对指定版本）。稳定 ID：同（提出者,文件:行,标题）
// 重报命中既有问题——未解：Occurrences/Replies 计数推进；已修（对当前版本）：
// 重开（回归），均不改 ID。公开壳：锁内实现完整返回（写锁已释放）后按实际变更
// 触发持久化通知——校验/锚定拒绝不通知。
func (e *Engine) RecordIssue(spaceID string, in IssueInput) (Issue, error) {
	is, err := e.recordIssue(spaceID, in)
	if err == nil {
		e.notifyPersist()
	}
	return is, err
}

// recordIssue RecordIssue 的锁内实现。
func (e *Engine) recordIssue(spaceID string, in IssueInput) (Issue, error) {
	if strings.TrimSpace(in.Reviewer) == "" {
		return Issue{}, revErrf(ErrIssueInvalid, "审查者必填")
	}
	if !in.Severity.Valid() {
		return Issue{}, revErrf(ErrIssueInvalid, "级别须为 must|should|info，得 %q", string(in.Severity))
	}
	if strings.TrimSpace(in.File) == "" {
		return Issue{}, revErrf(ErrIssueInvalid, "文件必填")
	}
	if in.Line < 0 {
		return Issue{}, revErrf(ErrIssueInvalid, "行号为负")
	}
	if strings.TrimSpace(in.Title) == "" {
		return Issue{}, revErrf(ErrIssueInvalid, "标题必填")
	}
	if !validHash(in.FoundInHash) {
		return Issue{}, revErrf(ErrIssueInvalid, "发现版本须为 SHA-256 hex64")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	now := e.now()
	// 发现版本须为已登记版本（W05-03 终审建议1，对照 RecordEvidence 锚定校验
	// 先例）：问题登记锚定发现版本并承担初审锚初始化——未登记/陈旧 hash 拒绝，
	// 封「初审锚指向不存在版本」的返工零进度乒乓形态。
	if e.revisionByHashLocked(lp, in.FoundInHash) == nil {
		return Issue{}, revErrf(ErrRevisionNotFound, "发现版本 %s… 未登记（问题须锚定本空间已登记版本）", tail(in.FoundInHash, 12))
	}
	// 初审锚（W05-03 全链驱动补定）：首条问题登记锚定其发现版本——初审也是审
	// 查，「最近审查锚」自初审起在位（返工→复审推进以「修复后新版本 ≠ 被审版
	// 本」为据，空锚会使首轮返工零进度循环推进）。已有锚不动：锚只被更新的审查
	// 事实（复审/零发现复审）推进，问题重报不回拨锚。
	if lp.LastReviewedHash == "" {
		lp.LastReviewedHash = in.FoundInHash
		lp.LastReviewedAt = now
	}
	id := issueID(in.Reviewer, strings.TrimSpace(in.File), in.Line, in.Title)
	for i := range lp.Issues {
		if lp.Issues[i].ID == id {
			// 重开投影判定先于序号推进（用推进前状态分留痕种类）。
			kind := TrailIssueRereported
			if IssueEffectiveState(&lp.Issues[i], in.FoundInHash) == IssueConfirmedFixed {
				kind = TrailIssueReopened
			}
			lp.Issues[i].Occurrences++
			lp.Issues[i].EventSeq++
			lp.Issues[i].LastReportedSeq = lp.Issues[i].EventSeq
			lp.Issues[i].UpdatedAt = now
			e.appendTrail(lp, kind, fmt.Sprintf("%s 第 %d 次报告（%s:%d）", id, lp.Issues[i].Occurrences, in.File, in.Line))
			return cloneIssue(&lp.Issues[i]), nil
		}
	}
	issue := Issue{
		ID:              id,
		Severity:        in.Severity,
		File:            strings.TrimSpace(in.File),
		Line:            in.Line,
		Title:           strings.TrimSpace(in.Title),
		Reviewer:        strings.TrimSpace(in.Reviewer),
		FoundInVersion:  in.FoundInHash,
		FoundInRound:    in.FoundInRound,
		CreatedAt:       now,
		UpdatedAt:       now,
		Occurrences:     1,
		EventSeq:        1,
		LastReportedSeq: 1,
		Note:            in.Note,
	}
	lp.Issues = append(lp.Issues, issue)
	e.appendTrail(lp, TrailIssueRecorded, fmt.Sprintf("%s [%s] %s:%d %s", id, string(in.Severity), issue.File, issue.Line, issue.Title))
	return cloneIssue(&issue), nil
}

// ReplyIssue 编写人回应问题（回复次数推进）。公开壳：锁内实现完整返回（写锁已
// 释放）后按实际变更触发持久化通知——问题不存在拒绝不通知。
func (e *Engine) ReplyIssue(spaceID, issueID, note string) error {
	if err := e.replyIssue(spaceID, issueID, note); err != nil {
		return err
	}
	e.notifyPersist()
	return nil
}

// replyIssue ReplyIssue 的锁内实现。
func (e *Engine) replyIssue(spaceID, issueID, note string) error {
	if strings.TrimSpace(note) == "" {
		return revErrf(ErrIssueInvalid, "回应内容必填")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	is := e.issueLocked(lp, issueID)
	if is == nil {
		return ErrIssueNotFound
	}
	is.Replies++
	is.UpdatedAt = e.now()
	e.appendTrail(lp, TrailIssueReplied, fmt.Sprintf("%s 回复第 %d 次", issueID, is.Replies))
	return nil
}

// ClaimFix 修复声明（返工：编写线程宣告已修，挂新版本 hash；等待复审）。公开壳：
// 锁内实现完整返回（写锁已释放）后按实际变更触发持久化通知——校验/问题不存在
// 拒绝不通知。
func (e *Engine) ClaimFix(spaceID, issueID, versionHash string, round int64, note string) error {
	if err := e.claimFix(spaceID, issueID, versionHash, round, note); err != nil {
		return err
	}
	e.notifyPersist()
	return nil
}

// claimFix ClaimFix 的锁内实现。
func (e *Engine) claimFix(spaceID, issueID, versionHash string, round int64, note string) error {
	if !validHash(versionHash) {
		return revErrf(ErrFixInvalid, "修复版本须为 SHA-256 hex64")
	}
	if round < 0 {
		return revErrf(ErrFixInvalid, "返工轮次为负")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	is := e.issueLocked(lp, issueID)
	if is == nil {
		return ErrIssueNotFound
	}
	is.Fixes = append(is.Fixes, FixAttempt{At: e.now(), Round: round, VersionHash: versionHash, Note: note})
	is.UpdatedAt = e.now()
	e.appendTrail(lp, TrailFixClaimed, fmt.Sprintf("%s 修复声明（版本 %s…，第 %d 次）", issueID, tail(versionHash, 12), len(is.Fixes)))
	return nil
}

// RecordReReview 登记复审结论（针对指定版本 hash）。同时回填最近一次待复核修复
// 声明的结论（修复次数推进的判定锚）；更新最近复审版本锚（版本匹配判定依据）。
// 公开壳：锁内实现完整返回（写锁已释放）后按实际变更触发持久化通知——校验/锚
// 定/问题不存在拒绝不通知。
func (e *Engine) RecordReReview(spaceID, issueID string, in ReReviewInput) error {
	if err := e.recordReReview(spaceID, issueID, in); err != nil {
		return err
	}
	e.notifyPersist()
	return nil
}

// recordReReview RecordReReview 的锁内实现。
func (e *Engine) recordReReview(spaceID, issueID string, in ReReviewInput) error {
	if strings.TrimSpace(in.Reviewer) == "" {
		return revErrf(ErrReReviewInvalid, "复审者必填")
	}
	if !validHash(in.AgainstHash) {
		return revErrf(ErrReReviewInvalid, "复审版本须为 SHA-256 hex64")
	}
	if !in.Result.Valid() {
		return revErrf(ErrReReviewInvalid, "复审结论须为 confirmed_fixed|still_open（invalidated 为版本变更投影，不可直写）")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	// 复审版本须为已登记版本（W05-03 终审建议1，对照 RecordEvidence 先例）：
	// 复审结论与最近审查锚只对真实版本推进。
	if e.revisionByHashLocked(lp, in.AgainstHash) == nil {
		return revErrf(ErrRevisionNotFound, "复审版本 %s… 未登记（复审须锚定本空间已登记版本）", tail(in.AgainstHash, 12))
	}
	is := e.issueLocked(lp, issueID)
	if is == nil {
		return ErrIssueNotFound
	}
	now := e.now()
	is.EventSeq++ // 复审事件序号 > LastReportedSeq 即清除重开投影（新一轮复审生效）
	is.ReReviews = append(is.ReReviews, ReReviewRecord{
		Seq: is.EventSeq, At: now, Reviewer: strings.TrimSpace(in.Reviewer), AgainstHash: in.AgainstHash,
		Result: in.Result, Note: in.Note,
	})
	for i := len(is.Fixes) - 1; i >= 0; i-- {
		if is.Fixes[i].Result == "" { // 最近一次待复核
			is.Fixes[i].Result = in.Result
			is.Fixes[i].VerifiedAgainst = in.AgainstHash
			break
		}
	}
	is.UpdatedAt = now
	lp.LastReviewedHash = in.AgainstHash
	lp.LastReviewedAt = now
	e.appendTrail(lp, TrailReReviewRecorded, fmt.Sprintf("%s 复审 %s（对 %s…）", issueID, string(in.Result), tail(in.AgainstHash, 12)))
	return nil
}

// CleanReviewRecord 空间级复审记录（终审建议3：「本轮复审零发现」——审查通过、
// 零发现是合法终态，必须有锚点表达，否则零问题空间完成门永远版本不匹配死路）。
type CleanReviewRecord struct {
	At          time.Time `json:"at"`
	Reviewer    string    `json:"reviewer"`
	AgainstHash string    `json:"against_hash"` // 复审针对版本
	Note        string    `json:"note,omitempty"`
}

// RecordCleanReview 登记空间级复审（空 issueID 语义的显式形态：本轮复审零发现）。
// 最近复审锚（LastReviewedHash/At）随之推进——完成判定的版本匹配由本锚承担，
// 与逐问题复审锚同通道。零发现不豁免其余完成输入（候选/证据/在途照查）。公开壳：
// 锁内实现完整返回（写锁已释放）后按实际变更触发持久化通知——校验/锚定拒绝不
// 通知。
func (e *Engine) RecordCleanReview(spaceID, reviewer, againstHash, note string) error {
	if err := e.recordCleanReview(spaceID, reviewer, againstHash, note); err != nil {
		return err
	}
	e.notifyPersist()
	return nil
}

// recordCleanReview RecordCleanReview 的锁内实现。
func (e *Engine) recordCleanReview(spaceID, reviewer, againstHash, note string) error {
	if strings.TrimSpace(reviewer) == "" {
		return revErrf(ErrReReviewInvalid, "复审者必填")
	}
	if !validHash(againstHash) {
		return revErrf(ErrReReviewInvalid, "复审版本须为 SHA-256 hex64")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lp := e.loop(spaceID, true)
	// 复审版本须为已登记版本（W05-03 终审建议1，对照 RecordEvidence 先例）：
	// 零发现复审锚同样只对真实版本推进。
	if e.revisionByHashLocked(lp, againstHash) == nil {
		return revErrf(ErrRevisionNotFound, "复审版本 %s… 未登记（复审须锚定本空间已登记版本）", tail(againstHash, 12))
	}
	now := e.now()
	lp.CleanReviews = append(lp.CleanReviews, CleanReviewRecord{
		At: now, Reviewer: strings.TrimSpace(reviewer), AgainstHash: againstHash, Note: note,
	})
	lp.LastReviewedHash = againstHash
	lp.LastReviewedAt = now
	e.appendTrail(lp, TrailCleanReview, fmt.Sprintf("本轮复审零发现（对 %s…，by %s）", tail(againstHash, 12), strings.TrimSpace(reviewer)))
	return nil
}

// IssueEffectiveState 问题有效状态（原始记录 + 版本匹配 + 重开投影；不另存状态
// 字段——单一事实源）：
//   - 无复审记录 → open；
//   - 最近复审之后存在同 key 重报（LastReportedSeq > 最近复审 Seq）→ open（回归
//     重开：重报已确认问题即回归，须对当前版本新一轮复审才再确认——终审必修1）；
//   - 最近复审 still_open → still_open（负结论不因版本变更「失效变好」）；
//   - 最近复审 confirmed_fixed 且针对当前版本 → confirmed_fixed；
//   - 最近复审 confirmed_fixed 但版本已变 → stale（旧 review 失效留痕：记录保留，
//     等待对新版本复审）。
func IssueEffectiveState(is *Issue, currentHash string) IssueState {
	if len(is.ReReviews) == 0 {
		return IssueOpen
	}
	last := is.ReReviews[len(is.ReReviews)-1]
	if is.LastReportedSeq > last.Seq {
		return IssueOpen // 回归重开（重报事实进投影；事件序号判先后，不依赖时钟同刻）
	}
	switch last.Result {
	case ReReviewStillOpen:
		return IssueStillOpen
	case ReReviewConfirmed:
		if last.AgainstHash == currentHash {
			return IssueConfirmedFixed
		}
		return IssueStale
	}
	return IssueOpen
}

// IssueBlocking 问题是否阻塞（must/should 且非「对当前版本的已修确认」）。
// info 级别不阻塞返工与完成。
func IssueBlocking(is *Issue, currentHash string) bool {
	if !is.Severity.Blocking() {
		return false
	}
	return IssueEffectiveState(is, currentHash) != IssueConfirmedFixed
}

// issueLocked 按 ID 查问题（调用方持锁）。
func (e *Engine) issueLocked(lp *SpaceLoop, id string) *Issue {
	for i := range lp.Issues {
		if lp.Issues[i].ID == id {
			return &lp.Issues[i]
		}
	}
	return nil
}

// cloneRevision / cloneIssue 深拷贝出包（防共享底层数组）。
func cloneRevision(r *Revision) Revision {
	out := *r
	if r.TestCLI != nil {
		c := *r.TestCLI
		out.TestCLI = &c
	}
	if r.TestProto != nil {
		c := *r.TestProto
		out.TestProto = &c
	}
	if r.TestRound != nil {
		c := *r.TestRound
		out.TestRound = &c
	}
	if r.TestAccept != nil {
		c := *r.TestAccept
		out.TestAccept = &c
	}
	if len(r.Dependencies) > 0 {
		out.Dependencies = append([]ArtifactRef(nil), r.Dependencies...)
	}
	return out
}

func cloneIssue(is *Issue) Issue {
	out := *is
	if len(is.Fixes) > 0 {
		out.Fixes = append([]FixAttempt(nil), is.Fixes...)
	}
	if len(is.ReReviews) > 0 {
		out.ReReviews = append([]ReReviewRecord(nil), is.ReReviews...)
	}
	return out
}

// tail 非空串尾 n 字节（空返回 "<无>"——日志/留痕人话形态）。
func tail(s string, n int) string {
	if s == "" {
		return "<无>"
	}
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
