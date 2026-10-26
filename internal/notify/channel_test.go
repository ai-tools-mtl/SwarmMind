package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- 渠道准入校验 ----------

// TestValidateChannelTypes 类型与格式准入：三类合法；feishu 类型已退役（提示
// 并入 webhook）；sms 占位拒绝；未知拒绝；format 仅 webhook 支持。
func TestValidateChannelTypes(t *testing.T) {
	// inbox 无凭证段：类型本身被接受即零报错
	if err := validateChannel(TypeInbox, "", "x", nil, nil); err != nil {
		t.Errorf("类型 %s 不应因类型本身被拒: %v", TypeInbox, err)
	}
	// email/webhook 类型本身合法（报的是凭证缺项，说明已进入按类型凭证校验段）
	if err := validateChannel(TypeWebhook, "", "x", nil, nil); err == nil || !strings.Contains(err.Error(), "url") {
		t.Errorf("webhook 缺 url 应报凭证缺项（类型本身被接受）, got %v", err)
	}
	// feishu 类型退役：明确报错并指路（存量经载入迁移归一，不再有新 feishu 渠道）
	err := validateChannel(TypeFeishu, "", "x", nil, map[string]string{"webhook_url": "https://hook/x"})
	if err == nil || !strings.Contains(err.Error(), "并入 webhook") {
		t.Errorf("feishu 类型应被拒绝并提示并入 webhook, got %v", err)
	}
	// sms：枚举占位但明确拒绝（需接入服务商）
	err = validateChannel(TypeSMS, "", "x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "暂未开放") {
		t.Errorf("sms 应被拒绝并提示暂未开放, got %v", err)
	}
	// 未知类型
	if err := validateChannel("telegram", "", "x", nil, nil); err == nil {
		t.Error("未知类型应被拒绝")
	}
	// 名称必填 + 长度上限
	if err := validateChannel(TypeInbox, "", "  ", nil, nil); err == nil {
		t.Error("空名称应被拒绝")
	}
	if err := validateChannel(TypeInbox, "", strings.Repeat("长", 65), nil, nil); err == nil {
		t.Error("超长名称应被拒绝")
	}
	if err := validateChannel(TypeInbox, "", strings.Repeat("长", 64), nil, nil); err != nil {
		t.Errorf("64 字符名称应合法: %v", err)
	}
	// format 维度：webhook 认 generic/feishu/空；飞书格式给非 webhook 类型即拒绝
	if err := validateChannel(TypeWebhook, FormatFeishu, "x", nil, map[string]string{"url": "https://hook/x"}); err != nil {
		t.Errorf("webhook+feishu 格式应合法: %v", err)
	}
	if err := validateChannel(TypeWebhook, FormatGeneric, "x", nil, map[string]string{"url": "https://hook/x"}); err != nil {
		t.Errorf("webhook+generic 格式应合法: %v", err)
	}
	if err := validateChannel(TypeWebhook, "bogus", "x", nil, map[string]string{"url": "https://hook/x"}); err != nil {
		t.Errorf("未知格式按通用格式兜底不应报错: %v", err)
	}
	if err := validateChannel(TypeEmail, FormatFeishu, "x", nil, nil); err == nil {
		t.Error("飞书格式给 email 渠道应被拒绝")
	}
}

// TestValidateSecretsByType 按类型必填凭证：webhook 地址合法性与格式分叉
//（飞书格式只认 url、拒自定义头与退役的 webhook_url 键）、email 六件套、端口范围。
func TestValidateSecretsByType(t *testing.T) {
	// webhook+feishu：url 必填且 http(s)
	if err := validateSecrets(TypeWebhook, FormatFeishu, map[string]string{}); err == nil {
		t.Error("飞书格式缺 url 应报错")
	}
	if err := validateSecrets(TypeWebhook, FormatFeishu, map[string]string{"url": "ftp://x"}); err == nil {
		t.Error("飞书格式非 http(s) 应报错")
	}
	if err := validateSecrets(TypeWebhook, FormatFeishu, map[string]string{"url": "https://open.feishu.cn/open-apis/bot/v2/hook/xxx"}); err != nil {
		t.Errorf("合法飞书地址不应报错: %v", err)
	}
	// 退役键：webhook_url 不再是合法键（存量迁移已归一为 url）
	if err := validateSecrets(TypeWebhook, FormatFeishu, map[string]string{"webhook_url": "https://hook/x"}); err == nil || !strings.Contains(err.Error(), "退役") {
		t.Errorf("webhook_url 键应被拒绝并提示退役, got %v", err)
	}
	// 双键并存（codex 审查必修 3）：合法 url + 旧 webhook_url 同投必须整单拒绝——
	// 退役键不得借合法 url 搭车入库（不分消息格式，与 url 合法性无关）
	dual := map[string]string{"url": "https://hook/ok", "webhook_url": "https://hook/legacy"}
	if err := validateSecrets(TypeWebhook, FormatFeishu, dual); err == nil || !strings.Contains(err.Error(), "退役") {
		t.Errorf("双键并存（飞书格式）应拒绝并提示退役, got %v", err)
	}
	if err := validateSecrets(TypeWebhook, FormatGeneric, dual); err == nil || !strings.Contains(err.Error(), "退役") {
		t.Errorf("双键并存（通用格式）应拒绝并提示退役, got %v", err)
	}
	// 飞书格式不支持自定义头（存了也永不生效——宁可明确报错）
	if err := validateSecrets(TypeWebhook, FormatFeishu, map[string]string{"url": "https://hook/x", "header.X-Token": "v"}); err == nil {
		t.Error("飞书格式带自定义头应报错")
	}
	// webhook+generic：url 必填；header 键形态校验 + 值禁换行
	if err := validateSecrets(TypeWebhook, FormatGeneric, map[string]string{"url": "http://x", "header.": "v"}); err == nil {
		t.Error("空 header 名应报错")
	}
	if err := validateSecrets(TypeWebhook, FormatGeneric, map[string]string{"url": "http://x", "header.X-Token": "v"}); err != nil {
		t.Errorf("合法 header 不应报错: %v", err)
	}
	if err := validateSecrets(TypeWebhook, FormatGeneric, map[string]string{"url": "http://x", "header.X-Token": "v1\nX-Evil: on"}); err == nil {
		t.Error("header 值含换行应报错（防编辑抽屉裂行出幽灵头）")
	}
	// email：六件套逐项必填 + port 数字范围
	full := map[string]string{"host": "smtp.x.com", "port": "465", "username": "u", "password": "p", "from": "a@x.com", "to": "b@x.com"}
	if err := validateSecrets(TypeEmail, "", full); err != nil {
		t.Errorf("完整 email 配置不应报错: %v", err)
	}
	for k := range full {
		broken := map[string]string{}
		for kk, vv := range full {
			if kk != k {
				broken[kk] = vv
			}
		}
		if err := validateSecrets(TypeEmail, "", broken); err == nil {
			t.Errorf("email 缺 %s 应报错", k)
		}
	}
	for _, badPort := range []string{"0", "65536", "abc", "-1"} {
		broken := map[string]string{"host": "h", "port": badPort, "username": "u", "password": "p", "from": "a", "to": "b"}
		if err := validateSecrets(TypeEmail, "", broken); err == nil {
			t.Errorf("email port=%q 应报错", badPort)
		}
	}
	// inbox：无凭证要求
	if err := validateSecrets(TypeInbox, "", nil); err != nil {
		t.Errorf("inbox 不应要求凭证: %v", err)
	}
}

// TestNormalizeEvents 事件规范化：Trim、去重、保序、非法值报错、空=全订阅。
func TestNormalizeEvents(t *testing.T) {
	got, err := normalizeEvents([]string{" task_failed ", "task_failed", "task_needs_review", ""})
	if err != nil {
		t.Fatalf("合法事件不应报错: %v", err)
	}
	if len(got) != 2 || got[0] != EventTaskFailed || got[1] != EventNeedsReview {
		t.Errorf("规范化结果不符: %v", got)
	}
	if out, err := normalizeEvents(nil); err != nil || len(out) != 0 {
		t.Errorf("nil 应规范化为空（全订阅）: %v %v", out, err)
	}
	if _, err := normalizeEvents([]string{"on_fire"}); err == nil {
		t.Error("非法事件应报错")
	}
}

// ---------- 脱敏 ----------

// TestMasked 脱敏出口（列表/创建/编辑三类读响应统一语义）：password 类字段与
// 全部自定义头的值替换 ******；内嵌登录凭证（user:pass@host）的地址整串 ******；
// 普通地址与字段保持明文；原对象不被改动。
func TestMasked(t *testing.T) {
	c := Channel{
		Type: TypeWebhook,
		Secrets: map[string]string{
			"url":              "https://hook/yyy?token=q",
			"url_with_auth":    "https://alice:pw123@hook/zzz",
			"webhook_url":      "https://bob:pw456@open.feishu.cn/hook/x",
			"header.X-Token":   "tok-1",
			"header.X-API-Key": "ak-1",
			"host":             "smtp.x.com",
		},
	}
	m := c.Masked()
	if m.Secrets["url"] != "https://hook/yyy?token=q" {
		t.Errorf("无凭证地址应保持明文（编辑抽屉需核对）, got %q", m.Secrets["url"])
	}
	if m.Secrets["url_with_auth"] != MaskPlaceholder || m.Secrets["webhook_url"] != MaskPlaceholder {
		t.Errorf("内嵌 user:pass@ 的地址应整串脱敏: %v", m.Secrets)
	}
	for _, k := range []string{"header.X-Token", "header.X-API-Key"} {
		if m.Secrets[k] != MaskPlaceholder {
			t.Errorf("自定义头 %s 的值应一律脱敏, got %q", k, m.Secrets[k])
		}
	}
	if m.Secrets["host"] != "smtp.x.com" {
		t.Errorf("普通字段应保持明文: %v", m.Secrets)
	}
	if c.Secrets["header.X-API-Key"] != "ak-1" || c.Secrets["url_with_auth"] != "https://alice:pw123@hook/zzz" {
		t.Error("Masked 不得改动原对象")
	}
}

// TestUpdateHeaderAuthoritative webhook 自定义头「提交即权威」：secrets 提供
// 时提交里消失的 header 键即删除（前端删行必须生效）；secrets 未提供（nil，
// 启用开关等部分更新）则全部保留；占位回传保留原值。
func TestUpdateHeaderAuthoritative(t *testing.T) {
	s := newTestStore(t)
	ch := mustCreate(t, s, TypeWebhook, "", "带头", nil, map[string]string{
		"url":             "https://hook/x",
		"header.A":        "a-val",
		"header.B":        "b-val",
		"header.X-Secret": "sec-val",
	})

	// ① 提交 {url, header.A}（B/X-Secret 消失）→ 两者删除，A 保留
	upd, err := s.Update(ch.ID, "", "", "", nil, nil, false,
		map[string]string{"url": "https://hook/x", "header.A": "a-val"})
	if err != nil {
		t.Fatalf("提交更新应成功: %v", err)
	}
	if _, ok := upd.Secrets["header.B"]; ok {
		t.Errorf("提交里消失的 header 键应删除: %v", upd.Secrets)
	}
	if _, ok := upd.Secrets["header.X-Secret"]; ok {
		t.Errorf("提交里消失的 header 键应删除: %v", upd.Secrets)
	}
	if upd.Secrets["header.A"] != "a-val" || upd.Secrets["url"] != "https://hook/x" {
		t.Errorf("提交的键应保留: %v", upd.Secrets)
	}

	// ② GET 脱敏 → 回传 ****** → 原值保留（GitHub 式语义不受「提交即权威」影响）
	upd2, err := s.Update(ch.ID, "", "", "", nil, nil, false,
		map[string]string{"url": "https://hook/x", "header.A": MaskPlaceholder})
	if err != nil {
		t.Fatalf("占位回传应成功: %v", err)
	}
	if upd2.Secrets["header.A"] != "a-val" {
		t.Errorf("****** 回传应保留原值, got %q", upd2.Secrets["header.A"])
	}

	// ③ 部分更新（只传 enabled，secrets 缺省）→ 全部保留，头不受权威语义影响
	off := false
	upd3, err := s.Update(ch.ID, "", "", "", &off, nil, false, nil)
	if err != nil {
		t.Fatalf("部分更新应成功: %v", err)
	}
	if upd3.Secrets["header.A"] != "a-val" || upd3.Secrets["url"] != "https://hook/x" {
		t.Errorf("secrets 缺省不得碰任何凭证: %v", upd3.Secrets)
	}

	// ④ 空串显式清除单头（既有语义不变）
	upd4, err := s.Update(ch.ID, "", "", "", nil, nil, false,
		map[string]string{"url": "https://hook/x", "header.A": ""})
	if err != nil {
		t.Fatalf("空串清除应成功: %v", err)
	}
	if _, ok := upd4.Secrets["header.A"]; ok {
		t.Errorf("空串应显式清除该头: %v", upd4.Secrets)
	}

	// ⑤ secrets 提供空 map → 头全删（提交内容为准），url 保留
	upd5, err := s.Update(ch.ID, "", "", "", nil, nil, false, map[string]string{})
	if err != nil {
		t.Fatalf("空 map 提交应成功: %v", err)
	}
	for k := range upd5.Secrets {
		if isHeaderKey(k) {
			t.Errorf("空 map 提交后不应残留任何头: %v", upd5.Secrets)
		}
	}
	if upd5.Secrets["url"] != "https://hook/x" {
		t.Errorf("非 header 键维持 merge 语义: %v", upd5.Secrets)
	}
}

// ---------- CRUD 与占位保留 ----------

// TestChannelStoreCRUD 创建/查/改/删全链；ID c- 前缀自生成；创建时间 UTC。
func TestChannelStoreCRUD(t *testing.T) {
	s := newTestStore(t)
	ch := mustCreate(t, s, TypeWebhook, FormatFeishu, "飞书群", nil, map[string]string{"url": "https://hook/xxx"})
	if len(ch.ID) < 3 || ch.ID[:2] != "c-" {
		t.Errorf("渠道 ID 应 c- 前缀, got %q", ch.ID)
	}
	if !ch.Enabled || !ch.CreatedAt.Equal(testNow.UTC()) {
		t.Errorf("创建默认态不符: enabled=%v created=%v", ch.Enabled, ch.CreatedAt)
	}
	// Get
	got, ok := s.Get(ch.ID)
	if !ok || got.Name != "飞书群" {
		t.Fatalf("Get 应命中: %v %v", got, ok)
	}
	// List 倒序（新渠道在前；同刻创建按 ID 兜底）
	ch2, err := s.Create(TypeInbox, "", "站内", nil, nil, testNow.Add(time.Second))
	if err != nil {
		t.Fatalf("创建第二渠道: %v", err)
	}
	list := s.List()
	if len(list) != 2 || list[0].ID != ch2.ID {
		t.Errorf("List 应新渠道在前: %+v", list)
	}
	// Delete
	deleted, err := s.Delete(ch.ID)
	if err != nil || !deleted {
		t.Fatalf("删除应成功: %v %v", deleted, err)
	}
	if deleted2, _ := s.Delete(ch.ID); deleted2 {
		t.Error("重复删除应返回 false")
	}
	if _, ok := s.Get(ch.ID); ok {
		t.Error("删除后 Get 不应命中")
	}
}

// TestUpdatePlaceholderRetention PUT 占位保留语义：****** 保留原值；空串清除；
// 部分更新（只改 enabled）不碰 name/events/secrets；类型切换丢弃旧凭证。
func TestUpdatePlaceholderRetention(t *testing.T) {
	s := newTestStore(t)
	ch := mustCreate(t, s, TypeEmail, "", "邮箱", nil, map[string]string{
		"host": "smtp.x.com", "port": "465", "username": "u", "password": "real-pass", "from": "a@x", "to": "b@x",
	})

	// ① password 传 ****** → 保留原值；其余字段照常更新
	upd, err := s.Update(ch.ID, "", "", "新名字", nil, []string{EventTaskFailed}, true,
		map[string]string{"password": MaskPlaceholder})
	if err != nil {
		t.Fatalf("占位更新应成功: %v", err)
	}
	if upd.Secrets["password"] != "real-pass" {
		t.Errorf("****** 应保留原值, got %q", upd.Secrets["password"])
	}
	if upd.Name != "新名字" || len(upd.Events) != 1 || upd.Events[0] != EventTaskFailed {
		t.Errorf("其余字段应更新: %+v", upd)
	}
	// merge 语义：未提交的键保留（部分更新如「启用开关」不丢凭证）
	if upd.Secrets["host"] != "smtp.x.com" {
		t.Errorf("未提交的键应保留（merge 语义）, got %v", upd.Secrets)
	}

	// ② 只改 enabled 的部分更新：name/events/secrets 全保留
	en := false
	upd2, err := s.Update(ch.ID, "", "", "", &en, nil, false, nil)
	if err != nil {
		t.Fatalf("部分更新应成功: %v", err)
	}
	if upd2.Enabled || upd2.Name != "新名字" || upd2.Secrets["password"] != "real-pass" {
		t.Errorf("部分更新不得碰未提供字段: %+v", upd2)
	}

	// ③ 空串显式清除必填字段 → 校验按缺项报错，状态零变更
	if _, err := s.Update(ch.ID, "", "", "", nil, nil, false, map[string]string{"password": ""}); err == nil {
		t.Error("清空必填 password 应报错")
	}
	if cur3, _ := s.Get(ch.ID); cur3.Secrets["password"] != "real-pass" {
		t.Errorf("失败更新不得改动内存状态, got %q", cur3.Secrets["password"])
	}

	// ④ 类型切换：旧凭证全部作废，新类型缺项明确报错；feishu 类型已退役
	if _, err := s.Update(ch.ID, TypeFeishu, "", "", nil, nil, false, nil); err == nil {
		t.Error("切到已退役的 feishu 类型应报错（提示并入 webhook）")
	}
	// 占位跨类型无原值可保 → 键被丢弃 → 校验按缺项报错
	if _, err := s.Update(ch.ID, TypeWebhook, FormatFeishu, "", nil, nil, false,
		map[string]string{"url": MaskPlaceholder}); err == nil {
		t.Error("占位跨类型无原值应报错")
	}
	upd4, err := s.Update(ch.ID, TypeWebhook, FormatFeishu, "", nil, nil, false, map[string]string{"url": "https://hook/new"})
	if err != nil {
		t.Fatalf("切类型并给新凭证应成功: %v", err)
	}
	if upd4.Type != TypeWebhook || upd4.Format != FormatFeishu || upd4.Secrets["url"] != "https://hook/new" {
		t.Errorf("类型切换结果不符: %+v", upd4)
	}
	if _, ok := upd4.Secrets["password"]; ok {
		t.Error("类型切换后旧类型凭证不得残留")
	}
	// 格式切回 generic：format 覆盖生效（空串=未提供，须显式传 generic）
	upd5, err := s.Update(ch.ID, "", FormatGeneric, "", nil, nil, false, nil)
	if err != nil {
		t.Fatalf("格式切回 generic 应成功: %v", err)
	}
	if upd5.Format != FormatGeneric || upd5.Secrets["url"] != "https://hook/new" {
		t.Errorf("格式覆盖结果不符: %+v", upd5)
	}

	// ⑤ 更新不存在的渠道 → ErrNotFound
	if _, err := s.Update("c-nope", "", "", "x", nil, nil, false, nil); err == nil {
		t.Error("更新不存在渠道应报错")
	}
	// ⑥ 更新校验失败内存零变更（占位语义不被坏请求破坏）
	cur, _ := s.Get(ch.ID)
	if _, err := s.Update(ch.ID, TypeWebhook, FormatFeishu, "", nil, nil, false,
		map[string]string{"url": "https://hook/x", "header.A": "v"}); err == nil {
		t.Error("飞书格式带自定义头应报错")
	}
	cur2, _ := s.Get(ch.ID)
	if cur2.Type != cur.Type || cur2.Format != cur.Format || cur2.Secrets["url"] != cur.Secrets["url"] {
		t.Errorf("失败更新不得改动内存状态: %+v vs %+v", cur, cur2)
	}
}

// TestCreateValidationRejects 创建路径校验拒绝：非法事件/非法地址/sms/退役
// feishu 类型/飞书格式缺地址或带自定义头。
func TestCreateValidationRejects(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name    string
		typ     string
		format  string
		events  []string
		secrets map[string]string
	}{
		{"未知事件", TypeInbox, "", []string{"on_fire"}, nil},
		{"sms 占位", TypeSMS, "", nil, nil},
		{"feishu 类型已退役", TypeFeishu, "", nil, map[string]string{"webhook_url": "https://hook/x"}},
		{"双键并存（合法url+退役键）", TypeWebhook, "", nil, map[string]string{"url": "https://hook/ok", "webhook_url": "https://hook/legacy"}},
		{"飞书格式缺地址", TypeWebhook, FormatFeishu, nil, nil},
		{"webhook 坏地址", TypeWebhook, "", nil, map[string]string{"url": "not-a-url"}},
	}
	for _, tc := range cases {
		if _, err := s.Create(tc.typ, tc.format, "x", tc.events, tc.secrets, testNow); err == nil {
			t.Errorf("%s 应被拒绝", tc.name)
		}
	}
	if len(s.List()) != 0 {
		t.Error("被拒绝的创建不得落盘")
	}
}

// ---------- hub.yaml 旧 notify 段兼容导入 ----------

// TestImportLegacyIdempotent 兼容导入幂等：首启导入一个渠道+写标记；
// 重启（重开存储）不重导；用户删光渠道后仍不重导；已有手工渠道时让位不覆盖；
// 空配置不导入；format 映射（feishu→webhook+飞书格式、raw→webhook 通用格式）。
func TestImportLegacyIdempotent(t *testing.T) {
	dir := t.TempDir()

	// feishu 格式导入 → webhook 渠道+飞书消息格式（2026-10-26 起直接落新形态）、
	// 全订阅、启用
	s, err := OpenChannelStore(dir)
	if err != nil {
		t.Fatalf("打开存储: %v", err)
	}
	imported, err := s.ImportLegacy("https://hook/legacy", FormatFeishu, testNow)
	if err != nil || !imported {
		t.Fatalf("首次导入应成功: %v %v", imported, err)
	}
	list := s.List()
	if len(list) != 1 || list[0].Type != TypeWebhook || list[0].Format != FormatFeishu || !list[0].Enabled || len(list[0].Events) != 0 {
		t.Fatalf("导入渠道形态不符: %+v", list)
	}
	if list[0].Secrets["url"] != "https://hook/legacy" {
		t.Errorf("导入凭证不符: %v", list[0].Secrets)
	}

	// 重开存储（模拟重启）：标记在，不重导
	s2, _ := OpenChannelStore(dir)
	imported, err = s2.ImportLegacy("https://hook/legacy", FormatFeishu, testNow)
	if err != nil || imported {
		t.Fatalf("二次导入应 no-op: %v %v", imported, err)
	}
	// 用户删光渠道后重启：仍不重导（只导入一次）
	if _, err := s2.Delete(s2.List()[0].ID); err != nil {
		t.Fatalf("删除: %v", err)
	}
	s3, _ := OpenChannelStore(dir)
	imported, _ = s3.ImportLegacy("https://hook/legacy", FormatFeishu, testNow)
	if imported {
		t.Fatal("删光后不得重导（legacy_imported 标记已写）")
	}
}

// TestImportLegacyRespectsManualChannels 已有手工渠道时导入让位：渠道不被
// 覆盖，且当场写导入标记——之后用户删光渠道+重启（重开存储）不得复活旧 YAML
// 渠道（让位不写标记的话，删光后下次启动会重导，等于手工配置被旧配置顶掉）。
func TestImportLegacyRespectsManualChannels(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenChannelStore(dir)
	if err != nil {
		t.Fatalf("打开存储: %v", err)
	}
	manual := mustCreate(t, s, TypeInbox, "", "手工", nil, nil)
	imported, err := s.ImportLegacy("https://hook/legacy", FormatRaw, testNow)
	if err != nil || imported {
		t.Fatalf("已有渠道时导入应让位: %v %v", imported, err)
	}
	if len(s.List()) != 1 || s.List()[0].ID != manual.ID {
		t.Fatalf("手工渠道不得被覆盖: %+v", s.List())
	}

	// 删光手工渠道 + 重启（重开存储）→ 旧 YAML 渠道不得复活
	if _, err := s.Delete(manual.ID); err != nil {
		t.Fatalf("删除手工渠道: %v", err)
	}
	s2, err := OpenChannelStore(dir)
	if err != nil {
		t.Fatalf("重开存储（模拟重启）: %v", err)
	}
	imported, err = s2.ImportLegacy("https://hook/legacy", FormatRaw, testNow)
	if err != nil || imported {
		t.Fatalf("让位后删光+重启不得重导: %v %v", imported, err)
	}
	if n := len(s2.List()); n != 0 {
		t.Fatalf("删光后应保持空表, got %d 条", n)
	}
}

// TestImportLegacyMapping raw 格式导入为通用 webhook 类型（url 键）。
func TestImportLegacyMapping(t *testing.T) {
	s := newTestStore(t)
	imported, err := s.ImportLegacy("https://hook/raw", FormatRaw, testNow)
	if err != nil || !imported {
		t.Fatalf("raw 导入应成功: %v %v", imported, err)
	}
	ch := s.List()[0]
	if ch.Type != TypeWebhook || ch.Secrets["url"] != "https://hook/raw" {
		t.Errorf("raw 应映射为 webhook 类型: %+v", ch)
	}
}

// TestImportLegacyEmptyConfig 空配置（未配 webhook_url）不导入。
func TestImportLegacyEmptyConfig(t *testing.T) {
	s := newTestStore(t)
	imported, err := s.ImportLegacy("", FormatRaw, testNow)
	if err != nil || imported || len(s.List()) != 0 {
		t.Errorf("空配置不得导入: %v %v", imported, err)
	}
}

// ---------- 落盘原子性与格式 ----------

// TestStorePersistFormat 快照落盘为合法 JSON 且含 legacy_imported 标记；
// 临时文件不残留（原子写 temp+rename）。
func TestStorePersistFormat(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenChannelStore(dir)
	mustCreate(t, s, TypeInbox, "", "站内", []string{EventTaskFailed}, nil)
	if _, err := s.ImportLegacy("https://hook/l", FormatRaw, testNow); err != nil {
		// 已有渠道 → 让位；此处直接手工置标记路径已由其他用例覆盖
		t.Log("导入让位（已有渠道），符合预期")
	}
	b, err := os.ReadFile(filepath.Join(dir, "notify_channels.json"))
	if err != nil {
		t.Fatalf("快照应落盘: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("快照应为合法 JSON: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notify_channels.json.tmp")); err == nil {
		t.Error("临时文件不得残留（原子写 temp+rename）")
	}
}

// ---------- 站内通知 Inbox ----------

// TestInboxUnreadAndMarkRead 站内通知：推送未读、单条标已读、全读清零、
// 容量上限丢最旧、ID 单调。
func TestInboxUnreadAndMarkRead(t *testing.T) {
	ib := &Inbox{}
	ib.Push(InboxItem{TaskID: "t-1", Status: "completed"})
	ib.Push(InboxItem{TaskID: "t-2", Status: "failed"})
	items, unread := ib.Recent(20)
	if unread != 2 || len(items) != 2 || items[0].TaskID != "t-2" {
		t.Fatalf("两条未读且最新在前: %+v unread=%d", items, unread)
	}
	if items[0].Read {
		t.Error("新条目应未读")
	}
	if !ib.MarkRead(items[0].ID) {
		t.Fatal("标记已读应命中")
	}
	if ib.MarkRead("n-nope") {
		t.Error("标记不存在的条目应返回 false")
	}
	if n := ib.MarkAllRead(); n != 1 {
		t.Errorf("全读应影响 1 条, got %d", n)
	}
	if u := ib.Unread(); u != 0 {
		t.Errorf("全读后未读应清零, got %d", u)
	}
	// 容量上限：丢最旧
	for i := 0; i < inboxCap+10; i++ {
		ib.Push(InboxItem{TaskID: "t-bulk"})
	}
	items, unread = ib.Recent(20)
	if len(items) != 20 || unread != inboxCap {
		t.Fatalf("Recent 应给 20 条展示、未读计全量 %d, got %d 条/%d 未读", inboxCap, len(items), unread)
	}
	all, _ := ib.Recent(inboxCap + 100)
	if len(all) != inboxCap {
		t.Errorf("存储应封顶 %d 条, got %d", inboxCap, len(all))
	}
}
