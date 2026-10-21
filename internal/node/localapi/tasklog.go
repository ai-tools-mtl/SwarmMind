// TaskLog 本机提交任务日志（Loop 7，INTERACTION 旅程②「消费者」的「我的任务」）：
// node 作为提交方经本地 API 派出的任务在本机留痕——hub 是任务真相源（查询走
// task_query 帧），日志只记「我派过什么」，供控制台列表与详情入口。
//
// 持久化 data_dir/submitted-tasks.json：互斥锁串行 + 临时文件 rename；条数有界
// （超出裁最旧），条目幂等（同 task_id 只留首条，幂等重试不重置创建时刻）。
package localapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// taskLogFileName 日志文件名（相对 data_dir）。
const taskLogFileName = "submitted-tasks.json"

// taskLogMax 日志条数上限（环形裁剪；任务真相在 hub，保留期 7 天，500 条足够
// 回环低量场景翻旧账）。
const taskLogMax = 500

// TaskLogEntry 本机提交的任务留痕单条。
type TaskLogEntry struct {
	// TaskID hub 分配的任务 ID（幂等命中时与原任务相同）。
	TaskID string `json:"task_id"`
	// TraceID 链路追踪 ID。
	TraceID string `json:"trace_id"`
	// TargetNode / TargetAgent 派单目标。
	TargetNode  string `json:"target_node"`
	TargetAgent string `json:"target_agent"`
	// SubmittedBy 提交标记（hub 侧按连接身份覆盖为 node_id，这里保留本机原始值）。
	SubmittedBy string `json:"submitted_by"`
	// IdemKey 幂等键（结果未知重试用）。
	IdemKey string `json:"idem_key,omitempty"`
	// CreatedAt 本机收到 submit_ack 的时刻（UTC；非 hub 创建时刻，近似值）。
	CreatedAt time.Time `json:"created_at"`
}

// TaskLog 本机提交任务日志（并发安全）。
type TaskLog struct {
	mu      sync.Mutex
	path    string
	tmp     string
	entries []TaskLogEntry // 创建时刻新→旧
}

// OpenTaskLog 打开（或创建）任务日志。
func OpenTaskLog(dataDir string) (*TaskLog, error) {
	t := &TaskLog{
		path: filepath.Join(dataDir, taskLogFileName),
		tmp:  filepath.Join(dataDir, taskLogFileName+".tmp"),
	}
	b, err := os.ReadFile(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localapi: 读取任务日志失败: %w", err)
	}
	if err := json.Unmarshal(b, &t.entries); err != nil {
		return nil, fmt.Errorf("localapi: 任务日志损坏 %s: %w", t.path, err)
	}
	sort.Slice(t.entries, func(i, j int) bool { return t.entries[i].CreatedAt.After(t.entries[j].CreatedAt) })
	return t, nil
}

// Record 登记一次提交（submit_ack 成功后调用）：同 task_id 幂等（重试命中不重复
// 留痕、不重置时刻）；超出上限裁最旧。落盘失败返回错误但内存已登记——日志是
// 便利视图，不阻塞提交主链路。
func (t *TaskLog) Record(e TaskLogEntry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = protocol.NowUTC()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.entries {
		if t.entries[i].TaskID == e.TaskID {
			return nil // 幂等命中：留痕已在
		}
	}
	t.entries = append([]TaskLogEntry{e}, t.entries...)
	if len(t.entries) > taskLogMax {
		t.entries = t.entries[:taskLogMax]
	}
	return t.persist()
}

// List 返回全部留痕（新→旧；副本，调用方可自由改写）。
func (t *TaskLog) List() []TaskLogEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]TaskLogEntry(nil), t.entries...)
}

// persist 落盘（临时文件+rename）；调用方持 mu。
func (t *TaskLog) persist() error {
	b, err := json.MarshalIndent(t.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("localapi: 任务日志编码失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return fmt.Errorf("localapi: 创建数据目录失败: %w", err)
	}
	if err := os.WriteFile(t.tmp, b, 0o644); err != nil {
		return fmt.Errorf("localapi: 写任务日志临时文件失败: %w", err)
	}
	if err := os.Rename(t.tmp, t.path); err != nil {
		return fmt.Errorf("localapi: 任务日志落盘失败: %w", err)
	}
	return nil
}
