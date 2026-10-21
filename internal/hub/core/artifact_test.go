package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/store"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// 产物取回中继单测（Loop 4，SPEC 六/4.2/4.8）：hub 只经 artifact_fetch/artifact_data
// 中继取回，node 侧哈希校验 + hub 侧再校验；超时/离线/缺失/冲突各有确定错误。

// newArtifactFixture 构造带一个终态任务（含 1 个产物登记）的 hub。任务经 store
// 事务直接置终态（跳过执行路径，聚焦中继）；node 端点是否在线由用例自行装配
//（bus 上创建端点 = 可达，不创建 = ErrNodeOffline）。
func newArtifactFixture(t *testing.T) (*hubFixture, string, protocol.Artifact, []byte) {
	f := newHub(t)
	if err := f.core.RegisterNode("nodeB", agentsOf("echo")); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	ack, err := f.core.Submit(submitReq("nodeB", "echo", "x"), "")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	content := []byte("artifact-bytes-001")
	sum := sha256.Sum256(content)
	meta := protocol.Artifact{Path: "outputs/x/result.txt", Sha256: hex.EncodeToString(sum[:]), Size: int64(len(content))}
	now := protocol.NowUTC()
	err = f.st.Update(func(st *store.State) error {
		tk := st.Tasks[ack.TaskID]
		tk.Status = protocol.StatusCompleted
		tk.Result = &protocol.TaskResult{Status: protocol.ResultCompleted, Artifacts: []protocol.Artifact{meta}, FinishedAt: now}
		st.Tasks[tk.ID] = tk
		return nil
	})
	if err != nil {
		t.Fatalf("置终态失败: %v", err)
	}
	return f, ack.TaskID, meta, content
}

func TestFetchArtifactRelay(t *testing.T) {
	f, taskID, meta, content := newArtifactFixture(t)
	ep := f.bus.Endpoint("nodeB")
	ep.Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgArtifactFetch {
			return
		}
		var p protocol.ArtifactFetchPayload
		if err := jsonUnmarshal(env.Payload, &p); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		b, _ := jsonMarshal(protocol.ArtifactDataPayload{
			TaskID: p.TaskID, Index: p.Index, Meta: meta,
			DataBase64: base64.StdEncoding.EncodeToString(content),
		})
		if err := ep.Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgArtifactData,
			MessageID: protocol.NewMessageID(), ReplyTo: env.ReplyTo, To: "hub", Payload: b,
		}); err != nil {
			t.Fatalf("artifact_data 发送失败: %v", err)
		}
	})

	art, err := f.core.FetchArtifact(taskID, 0, 5*time.Second)
	if err != nil {
		t.Fatalf("取回失败: %v", err)
	}
	if string(art.Data) != string(content) || art.Meta.Path != meta.Path || art.Meta.Sha256 != meta.Sha256 {
		t.Errorf("产物不符: meta=%+v data=%q", art.Meta, art.Data)
	}
}

func TestFetchArtifactHashMismatch(t *testing.T) {
	f, taskID, meta, _ := newArtifactFixture(t)
	ep := f.bus.Endpoint("nodeB")
	ep.Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgArtifactFetch {
			return
		}
		var p protocol.ArtifactFetchPayload
		if err := jsonUnmarshal(env.Payload, &p); err != nil {
			return
		}
		b, _ := jsonMarshal(protocol.ArtifactDataPayload{
			TaskID: p.TaskID, Index: p.Index, Meta: meta,
			DataBase64: base64.StdEncoding.EncodeToString([]byte("tampered-content")),
		})
		_ = ep.Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgArtifactData,
			MessageID: protocol.NewMessageID(), ReplyTo: env.ReplyTo, To: "hub", Payload: b,
		})
	})
	if _, err := f.core.FetchArtifact(taskID, 0, 5*time.Second); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("应报 ErrArtifactMismatch, got %v", err)
	}
}

func TestFetchArtifactNodeSaysNotFound(t *testing.T) {
	f, taskID, _, _ := newArtifactFixture(t)
	ep := f.bus.Endpoint("nodeB")
	ep.Subscribe(func(env *protocol.Envelope) {
		if env.Type != protocol.MsgArtifactFetch {
			return
		}
		_ = ep.Send("hub", &protocol.Envelope{
			Version: protocol.ProtocolVersion, Type: protocol.MsgErr,
			MessageID: protocol.NewMessageID(), ReplyTo: env.ReplyTo, To: "hub",
			Err: &protocol.ErrInfo{Code: protocol.CodeArtifactNotFound, Msg: "文件缺失"},
		})
	})
	if _, err := f.core.FetchArtifact(taskID, 0, 5*time.Second); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("应报 ErrArtifactNotFound, got %v", err)
	}
}

func TestFetchArtifactTimeout(t *testing.T) {
	f, taskID, _, _ := newArtifactFixture(t)
	ep := f.bus.Endpoint("nodeB")
	ep.Subscribe(func(*protocol.Envelope) {}) // 可达但不应答 → 等待至超时
	if _, err := f.core.FetchArtifact(taskID, 0, 50*time.Millisecond); !errors.Is(err, ErrFetchTimeout) {
		t.Fatalf("应报 ErrFetchTimeout, got %v", err)
	}
}

func TestFetchArtifactOfflineAndMissing(t *testing.T) {
	f, taskID, _, _ := newArtifactFixture(t)
	// 端点不存在（未在 bus 上创建 nodeB）→ ErrNodeOffline
	if _, err := f.core.FetchArtifact(taskID, 0, time.Second); !errors.Is(err, ErrNodeOffline) {
		t.Fatalf("应报 ErrNodeOffline, got %v", err)
	}
	// 任务不存在 → ErrTaskNotFound
	if _, err := f.core.FetchArtifact("t-nonexistent", 0, time.Second); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("应报 ErrTaskNotFound, got %v", err)
	}
	// 下标越界 → ErrArtifactNotFound
	ep := f.bus.Endpoint("nodeB")
	ep.Subscribe(func(*protocol.Envelope) {}) // 在线但下标检查先于发送
	if _, err := f.core.FetchArtifact(taskID, 9, time.Second); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("应报 ErrArtifactNotFound, got %v", err)
	}
}
