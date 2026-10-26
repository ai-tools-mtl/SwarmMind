// 存量 feishu 渠道迁移单测（2026-10-26 feishu 并入 webhook，TASK-notify-unify
// 改动二「无声硬要求」）：构造旧形态 JSON → Load → 断言新形态+字段归一+行为
// 不变；升级生产（伦哥 hub 已配飞书渠道）时渠道静默可用，零手工干预。
package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// writeRawChannels 直接落一份旧形态快照（绕过当前结构校验，模拟升级前的盘上事实）。
func writeRawChannels(t *testing.T, dir string, raw string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "notify_channels.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("落旧形态快照: %v", err)
	}
}

// TestMigrateLegacyFeishu 迁移主链：旧 Type=feishu（webhook_url 凭证）→ 载入即
// 归一为 Type=webhook + Format=feishu + Secrets["url"]（webhook_url 键删除），
// 名称/事件/启用态/创建时间原样保留；落盘即新形态（重开零再迁移）。
func TestMigrateLegacyFeishu(t *testing.T) {
	dir := t.TempDir()
	writeRawChannels(t, dir, `{
  "channels": [
    {
      "id": "c-legacy01",
      "type": "feishu",
      "name": "伦哥的飞书群",
      "enabled": true,
      "events": ["task_failed"],
      "secrets": {"webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/tok"},
      "created_at": "2026-10-01T08:00:00Z"
    },
    {
      "id": "c-keep0001",
      "type": "webhook",
      "name": "通用渠道",
      "enabled": false,
      "events": [],
      "secrets": {"url": "https://example.com/hook", "header.X-Token": "v"},
      "created_at": "2026-10-02T08:00:00Z"
    }
  ],
  "legacy_imported": true
}`)

	s, err := OpenChannelStore(dir)
	if err != nil {
		t.Fatalf("载入旧形态存储失败: %v", err)
	}
	byID := map[string]Channel{}
	for _, c := range s.List() {
		byID[c.ID] = c
	}
	mig, ok := byID["c-legacy01"]
	if !ok {
		t.Fatal("迁移渠道应保留原 ID")
	}
	if mig.Type != TypeWebhook || mig.Format != FormatFeishu {
		t.Errorf("迁移后类型/格式不符: type=%q format=%q", mig.Type, mig.Format)
	}
	if mig.Secrets["url"] != "https://open.feishu.cn/open-apis/bot/v2/hook/tok" {
		t.Errorf("地址应归一为 url 键: %v", mig.Secrets)
	}
	if _, ok := mig.Secrets["webhook_url"]; ok {
		t.Errorf("webhook_url 键应删除: %v", mig.Secrets)
	}
	if mig.Name != "伦哥的飞书群" || !mig.Enabled || len(mig.Events) != 1 || mig.Events[0] != EventTaskFailed {
		t.Errorf("迁移不得碰名称/启用态/事件订阅: %+v", mig)
	}
	// 相邻通用渠道不得被误伤（header 自定义头保留、enabled 原样）
	keep, ok := byID["c-keep0001"]
	if !ok || keep.Type != TypeWebhook || keep.Format != "" || keep.Secrets["header.X-Token"] != "v" || keep.Enabled {
		t.Errorf("非 feishu 渠道应原样保留: %+v %+v", ok, keep)
	}

	// 盘上落新形态：文件里不再有 feishu 类型
	b, err := os.ReadFile(filepath.Join(dir, "notify_channels.json"))
	if err != nil {
		t.Fatalf("读迁移后快照: %v", err)
	}
	if string(b) == "" {
		t.Fatal("迁移应落盘")
	}
	var raw struct {
		Channels []struct {
			Type   string            `json:"type"`
			Format string            `json:"format"`
			Secrets map[string]string `json:"secrets"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("快照应为合法 JSON: %v", err)
	}
	for _, c := range raw.Channels {
		if c.Type == TypeFeishu {
			t.Errorf("落盘后不应再有 feishu 类型: %s", c.Type)
		}
	}

	// 行为不变：迁移后渠道按飞书格式投递到原地址（deliver 消息体正确，DoD②）
	h := newHookServer(t, 0)
	if _, err := s.Update(mig.ID, "", "", "", nil, nil, false, map[string]string{"url": h.srv.URL}); err != nil {
		t.Fatalf("把迁移渠道指向测试收包服务: %v", err)
	}
	d, _ := newTestDispatcher(t, s)
	// 迁移渠道订阅 task_failed（原样保留的事件订阅），发 failed 验证触达
	d.TaskFinal(testTask(protocol.StatusFailed, &protocol.TaskResult{Status: protocol.ResultFailed}))
	d.WaitIdle()
	var m feishuMessage
	if err := json.Unmarshal([]byte(h.lastBody()), &m); err != nil {
		t.Fatalf("迁移渠道应仍发飞书格式消息: %v", err)
	}
	if m.MsgType != "text" || m.Content.Text == "" {
		t.Errorf("飞书消息形态不符: %+v", m)
	}
}

// TestMigrateLegacyFeishuIdempotent 幂等：迁移后重开（模拟重启）零再迁移、
// 快照零变更（无 feishu 类型即无 Update 写盘）。
func TestMigrateLegacyFeishuIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeRawChannels(t, dir, `{
  "channels": [
    {"id": "c-legacy01", "type": "feishu", "name": "群", "enabled": true,
     "events": [], "secrets": {"webhook_url": "https://hook/x"}, "created_at": "2026-10-01T08:00:00Z"}
  ]
}`)
	if _, err := OpenChannelStore(dir); err != nil {
		t.Fatalf("首次载入: %v", err)
	}
	b1, err := os.ReadFile(filepath.Join(dir, "notify_channels.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenChannelStore(dir); err != nil {
		t.Fatalf("重开（模拟重启）: %v", err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, "notify_channels.json"))
	if string(b1) != string(b2) {
		t.Error("已迁移的快照重开时不应再写盘（幂等）")
	}
	var raw struct {
		Channels []struct {
			Type   string `json:"type"`
			Format string `json:"format"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(b2, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Channels) != 1 || raw.Channels[0].Type != TypeWebhook || raw.Channels[0].Format != FormatFeishu {
		t.Errorf("重开后形态应保持新形态: %+v", raw.Channels)
	}
}

// TestMigrateLegacyFeishuNilSecrets 脏数据防御：secrets 为 null 的旧 feishu 条目
// 迁移不 panic（url 落空串，投递路径照旧报「缺少推送地址」，与迁移前行为一致）。
func TestMigrateLegacyFeishuNilSecrets(t *testing.T) {
	dir := t.TempDir()
	writeRawChannels(t, dir, `{
  "channels": [
    {"id": "c-legacy01", "type": "feishu", "name": "群", "enabled": true,
     "events": [], "secrets": null, "created_at": "2026-10-01T08:00:00Z"}
  ]
}`)
	s, err := OpenChannelStore(dir)
	if err != nil {
		t.Fatalf("载入: %v", err)
	}
	ch := s.List()[0]
	if ch.Type != TypeWebhook || ch.Format != FormatFeishu || ch.Secrets == nil {
		t.Errorf("脏数据迁移形态: %+v", ch)
	}
}

// TestAlsoNotifyDefault 同时通知开关默认语义：未写文件 = true（跟发）；
// 写 false 落盘可读回（cmd/node gate 测试覆盖热路径，此处锁存储语义）。
func TestAlsoNotifyDefault(t *testing.T) {
	dir := t.TempDir()
	if !AlsoNotify(dir) {
		t.Fatal("未配置应默认 true")
	}
	if err := SetAlsoNotify(dir, false); err != nil {
		t.Fatalf("写开关: %v", err)
	}
	if AlsoNotify(dir) {
		t.Fatal("false 应可读回")
	}
	// 落盘形态：{"also_notify": false}
	b, err := os.ReadFile(filepath.Join(dir, "notify_settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("开关文件应为合法 JSON: %v", err)
	}
	if v, ok := raw["also_notify"].(bool); !ok || v {
		t.Errorf("开关文件形态不符: %v", raw)
	}
}
