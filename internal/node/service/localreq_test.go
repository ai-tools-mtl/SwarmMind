package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/node/ledger"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// 本地请求关联与产物取回单测（Loop 4）：SubmitLocal/QueryLocal 走真 hub core +
// membus 同步链；artifact 取回走真账本 + 本地文件。

// recorder 帧录制器（双向订阅并发触达，带锁）。
type recorder struct {
	mu     sync.Mutex
	frames []*protocol.Envelope
}

func (r *recorder) record(env *protocol.Envelope) {
	cp := *env
	r.mu.Lock()
	r.frames = append(r.frames, &cp)
	r.mu.Unlock()
}

func (r *recorder) ofTypes(typ string) *protocol.Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.frames {
		if e.Type == typ {
			return e
		}
	}
	return nil
}

// newLocalFixture 真 hub（core）+ node service 全链基座（membus 同步）。
// 返回 artRoot 为 ArtifactRoots["echo"] 的产物根目录（测试写产物文件用）。
func newLocalFixture(t *testing.T) (*core.Core, *Service, *ledger.Ledger, *recorder, *transport.Endpoint, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus := transport.NewBus()
	c, err := core.New(core.Config{Store: st, Transport: bus.Endpoint("hub"), SweepInterval: 0})
	if err != nil {
		t.Fatalf("构造 core 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("打开账本失败: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })

	rec := &recorder{}
	// 双向录制（订阅以最后一次为准，包装保证录制与业务处理都生效）
	bus.Endpoint("hub").Subscribe(func(env *protocol.Envelope) {
		rec.record(env)
		c.HandleFrame(env)
	})

	artRoot := t.TempDir()
	nodeEP := bus.Endpoint("nodeA")
	svc := New(Config{Ledger: led, Runners: map[string]Runner{}, ArtifactRoots: map[string]string{
		"echo": artRoot,
	}}, nodeEP)
	t.Cleanup(svc.Close)
	nodeEP.Subscribe(func(env *protocol.Envelope) {
		rec.record(env)
		svc.HandleFrame(env)
	})

	if err := c.RegisterNode("nodeA", []protocol.AgentCard{{
		Name: "echo", Kind: protocol.AgentKindCLI, InputMode: "prompt_text",
		OutputMode: "answer+artifacts", PermissionMode: "no-shell", ProtocolVer: 1, Exposed: true,
	}}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	c.SetOnline("nodeA", true)
	return c, svc, led, rec, nodeEP, artRoot
}

func TestSubmitLocalAndQueryLocal(t *testing.T) {
	_, svc, _, rec, _, _ := newLocalFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := protocol.TaskSubmitRequest{
		TargetNode: "nodeA", TargetAgent: "echo", SubmittedBy: "local-proc",
		Input: protocol.TaskInput{Args: []string{"hi"}}, IdemKey: "loc-1",
	}
	ack, err := svc.SubmitLocal(ctx, req)
	if err != nil {
		t.Fatalf("SubmitLocal 失败: %v", err)
	}
	if ack.TaskID == "" || ack.TraceID == "" || ack.Dup {
		t.Fatalf("submit_ack 不符: %+v", ack)
	}
	// 请求-应答关联：submit_ack.ReplyTo = task_submit 的 mid
	submit := rec.ofTypes(protocol.MsgTaskSubmit)
	ackFrame := rec.ofTypes(protocol.MsgSubmitAck)
	if submit == nil || ackFrame == nil || ackFrame.ReplyTo != submit.MessageID {
		t.Fatalf("task_submit/submit_ack 未按 ReplyTo 成对: submit=%v ack=%v", submit, ackFrame)
	}

	// 幂等命中：Dup=true 且同 task_id（同键重试安全，SPEC 4.5）
	ack2, err := svc.SubmitLocal(ctx, req)
	if err != nil || !ack2.Dup || ack2.TaskID != ack.TaskID {
		t.Fatalf("幂等命中不符: ack=%+v err=%v", ack2, err)
	}

	// 查询：task_status 回完整任务（无 runner → agent_not_found 失败终态）。
	// 执行异步（并发 goroutine），轮询至终态（有上限替代固定 sleep）。
	var task *protocol.Task
	deadline := time.Now().Add(5 * time.Second)
	for {
		var qerr error
		task, qerr = svc.QueryLocal(ctx, ack.TaskID)
		if qerr != nil {
			t.Fatalf("QueryLocal 失败: %v", qerr)
		}
		if task.Status.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务未在期限内到达终态: %+v", task)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if task.ID != ack.TaskID || task.Status != protocol.StatusFailed || task.Result == nil ||
		task.Result.Error != protocol.CodeAgentNotFound {
		t.Fatalf("task_status 不符: %+v", task)
	}

	// 查无此任务 → RemoteError(task_not_found)
	_, err = svc.QueryLocal(ctx, "t-missing")
	var re *RemoteError
	if !errors.As(err, &re) || re.Code != protocol.CodeTaskNotFound {
		t.Fatalf("应报 RemoteError(task_not_found), got %v", err)
	}
}

func TestSubmitLocalHubUnreachable(t *testing.T) {
	t.Helper()
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	ep := bus.Endpoint("lonely") // bus 上无 "hub" 端点 → Send ErrNoEndpoint
	svc := New(Config{Ledger: led, Runners: map[string]Runner{}}, ep)
	t.Cleanup(svc.Close)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = svc.SubmitLocal(ctx, protocol.TaskSubmitRequest{TargetNode: "x", TargetAgent: "y", SubmittedBy: "z"})
	if !errors.Is(err, ErrHubUnreachable) {
		t.Fatalf("应报 ErrHubUnreachable, got %v", err)
	}
}

// artifactEntry 在账本落 finished 条目（含产物登记）并写对应文件；返回登记与文件目录。
func artifactEntry(t *testing.T, led *ledger.Ledger, artRoot, taskID, content string) (protocol.Artifact, string) {
	t.Helper()
	workdir := artRoot
	rel := "outputs/" + taskID + "/out.txt"
	abs := filepath.Join(workdir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	meta := protocol.Artifact{Path: rel, Sha256: hex.EncodeToString(sum[:]), Size: int64(len(content))}
	task := protocol.Task{ID: taskID, TraceID: "tr-" + taskID, TargetNode: "nodeA", TargetAgent: "echo", Status: protocol.StatusRunning}
	if err := led.Accept(task); err != nil {
		t.Fatal(err)
	}
	if err := led.MarkStarted(taskID); err != nil {
		t.Fatal(err)
	}
	res := protocol.TaskResult{Status: protocol.ResultCompleted, Artifacts: []protocol.Artifact{meta}}
	if err := led.MarkFinished(taskID, res); err != nil {
		t.Fatal(err)
	}
	return meta, workdir
}

// fetchEnvelope 构造 hub→node 的 artifact_fetch 帧。
func fetchEnvelope(mid, taskID string, index int) *protocol.Envelope {
	b, err := json.Marshal(protocol.ArtifactFetchPayload{TaskID: taskID, Index: index})
	if err != nil {
		panic(err) // 测试构造，不可达
	}
	return &protocol.Envelope{
		Version: protocol.ProtocolVersion, Type: protocol.MsgArtifactFetch,
		MessageID: mid, From: "hub", To: "nodeA", Payload: b,
	}
}

func TestHandleArtifactFetchOK(t *testing.T) {
	_, svc, led, rec, _, artRoot := newLocalFixture(t)
	content := "file-bytes-001"
	meta, _ := artifactEntry(t, led, artRoot, "t-art-1", content)

	svc.HandleFrame(fetchEnvelope("mid-1", "t-art-1", 0)) // hub→node 帧：直接调 HandleFrame

	data := rec.ofTypes(protocol.MsgArtifactData)
	if data == nil || data.ReplyTo != "mid-1" {
		t.Fatalf("应回 artifact_data 且 ReplyTo=mid-1: %+v", data)
	}
	var p protocol.ArtifactDataPayload
	if err := json.Unmarshal(data.Payload, &p); err != nil {
		t.Fatalf("载荷解析失败: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(p.DataBase64)
	if err != nil || string(raw) != content {
		t.Fatalf("产物内容不符: %q err=%v", p.DataBase64, err)
	}
	if p.Meta.Path != meta.Path || p.Meta.Sha256 != meta.Sha256 {
		t.Fatalf("meta 不符: %+v", p.Meta)
	}
}

func TestHandleArtifactFetchMismatchAndMissing(t *testing.T) {
	_, svc, led, rec, _, artRoot := newLocalFixture(t)

	// 内容被篡改：SHA-256 与登记不一致 → err(artifact_mismatch)
	meta, _ := artifactEntry(t, led, artRoot, "t-art-2", "original")
	abs := filepath.Join(artRoot, filepath.FromSlash(meta.Path))
	if err := os.WriteFile(abs, []byte("tampered!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.HandleFrame(fetchEnvelope("mid-2", "t-art-2", 0))
	errFrame := rec.ofTypes(protocol.MsgErr)
	if errFrame == nil || errFrame.ReplyTo != "mid-2" || errFrame.Err == nil ||
		errFrame.Err.Code != protocol.CodeArtifactMismatch {
		t.Fatalf("应回 err(artifact_mismatch) ReplyTo=mid-2: %+v", errFrame)
	}

	// 路径穿越拒绝（登记相对路径越界）→ err(artifact_mismatch)
	task := protocol.Task{ID: "t-art-3", TargetNode: "nodeA", TargetAgent: "echo", Status: protocol.StatusRunning}
	if err := led.Accept(task); err != nil {
		t.Fatal(err)
	}
	_ = led.MarkStarted("t-art-3")
	bad := protocol.Artifact{Path: "../../escape.txt", Sha256: "00", Size: 2}
	_ = led.MarkFinished("t-art-3", protocol.TaskResult{Status: protocol.ResultCompleted, Artifacts: []protocol.Artifact{bad}})
	rec.mu.Lock()
	rec.frames = nil
	rec.mu.Unlock()
	svc.HandleFrame(fetchEnvelope("mid-3", "t-art-3", 0))
	errFrame = rec.ofTypes(protocol.MsgErr)
	if errFrame == nil || errFrame.Err == nil || errFrame.Err.Code != protocol.CodeArtifactMismatch {
		t.Fatalf("穿越路径应拒绝: %+v", errFrame)
	}

	// 任务不存在 → err(artifact_not_found)
	rec.mu.Lock()
	rec.frames = nil
	rec.mu.Unlock()
	svc.HandleFrame(fetchEnvelope("mid-4", "t-none", 0))
	errFrame = rec.ofTypes(protocol.MsgErr)
	if errFrame == nil || errFrame.Err == nil || errFrame.Err.Code != protocol.CodeArtifactNotFound {
		t.Fatalf("应回 err(artifact_not_found): %+v", errFrame)
	}
}

