// 渠道存储：data_dir/notify_channels.json，经 settings.Store 原子读写（temp+
// rename），运行时可改——Dispatcher 每次终态事件都取最新快照，控制台增删改即
// 时生效，无需重启。hub 与 node（standalone）共用同一存储形态（各自 data_dir
// 下同名文件，互不相干）。
//
// hub.yaml 旧 notify 段向后兼容（hub 专用，node 无此配置源不调用）：启动时若
// 配置了 webhook_url 且尚未导入 → 渠道存储为空则导入为一个 feishu/raw 渠道，
// 已有手工渠道则让位；两种结局都写 legacy_imported 标记（含让位——否则删光
// 渠道+重启会复活旧 YAML 渠道），此后无论渠道被改成什么样（含全部删除）都不
// 再重导——「只导入一次」。
package notify

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/settings"
)

// ErrInvalid 渠道配置非法（HTTP 400）；ErrNotFound 渠道不存在（HTTP 404）。
var (
	ErrInvalid  = errors.New("notify: 渠道配置非法")
	ErrNotFound = errors.New("notify: 渠道不存在")
)

// channelsFile notify_channels.json 快照根结构。
type channelsFile struct {
	Channels []Channel `json:"channels"`
	// LegacyImported hub.yaml 旧 notify 段一次性导入标记（幂等锚）。
	LegacyImported bool `json:"legacy_imported,omitempty"`
}

// ChannelStore 渠道配置存储（settings.Store 泛型抽象的第一个用户）。
type ChannelStore struct {
	st *settings.Store[channelsFile]
}

// OpenChannelStore 打开 data_dir 下的渠道存储；文件不存在则从空表起步。打开即
// 执行存量 feishu 渠道归一迁移（migrateLegacyFeishu）。
func OpenChannelStore(dataDir string) (*ChannelStore, error) {
	st, err := settings.Open[channelsFile](dataDir, "notify_channels.json")
	if err != nil {
		return nil, err
	}
	s := &ChannelStore{st: st}
	if err := s.migrateLegacyFeishu(); err != nil {
		return nil, err
	}
	return s, nil
}

// migrateLegacyFeishu 存量独立 feishu 渠道一次性归一（2026-10-26 feishu 并入
// webhook，无声硬要求：伦哥生产 hub 已配的飞书渠道升级后必须无缝继续工作）：
// Type=feishu → Type=webhook + Format=feishu + Secrets["url"]=旧 webhook_url
//（webhook_url 键删除）。内存态立即生效（Dispatcher 读到即按飞书格式投递，
// 地址与消息体都不变——生产行为零跳变）；有迁移才落盘（Update 全量序列化即落
// 新形态），幂等：新形态条目不含 feishu 类型，重开零写入。
func (s *ChannelStore) migrateLegacyFeishu() error {
	has := false
	for _, c := range s.st.View().Channels {
		if c.Type == TypeFeishu {
			has = true
			break
		}
	}
	if !has {
		return nil
	}
	return s.st.Update(func(f *channelsFile) error {
		for i := range f.Channels {
			c := &f.Channels[i]
			if c.Type != TypeFeishu {
				continue
			}
			if c.Secrets == nil {
				c.Secrets = map[string]string{}
			}
			c.Secrets["url"] = c.Secrets["webhook_url"]
			delete(c.Secrets, "webhook_url")
			c.Type = TypeWebhook
			c.Format = FormatFeishu
		}
		return nil
	})
}

// List 返回全部渠道副本（创建时间倒序，新渠道在前；展示序确定化）。
func (s *ChannelStore) List() []Channel {
	f := s.st.View()
	out := append([]Channel(nil), f.Channels...)
	for i := range out {
		out[i] = out[i].clone()
	}
	// 倒序稳定排序：新渠道在前；同刻创建按 ID 兜底防 map/文件序抖动
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			a, b := out[i], out[j]
			if a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID > b.ID) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Get 按渠道 ID 取副本。
func (s *ChannelStore) Get(id string) (Channel, bool) {
	f := s.st.View()
	for _, c := range f.Channels {
		if c.ID == id {
			return c.clone(), true
		}
	}
	return Channel{}, false
}

// Create 校验并创建渠道；ID 自生成（c- 前缀），创建时间取 now。format 仅
// webhook 类型有意义（generic/feishu，空=generic）。
func (s *ChannelStore) Create(typ, format, name string, events []string, secrets map[string]string, now time.Time) (Channel, error) {
	if err := validateChannel(typ, format, name, events, secrets); err != nil {
		return Channel{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	norm, err := normalizeEvents(events)
	if err != nil {
		return Channel{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	id, err := newChannelID()
	if err != nil {
		return Channel{}, err
	}
	ch := Channel{
		ID:        id,
		Type:      typ,
		Format:    formatFor(typ, format),
		Name:      strings.TrimSpace(name),
		Enabled:   true,
		Events:    norm,
		Secrets:   copySecrets(secrets),
		CreatedAt: now.UTC(),
	}
	err = s.st.Update(func(f *channelsFile) error {
		f.Channels = append(f.Channels, ch)
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	return ch, nil
}

// formatFor 渠道落盘格式：webhook 归一（generic/feishu），其余类型恒空。
func formatFor(typ, format string) string {
	if typ != TypeWebhook {
		return ""
	}
	return normalizeFormat(format)
}

// Update 渠道更新（PUT 语义：占位保留凭证，见 applyUpdate）。id 不存在 →
// ErrNotFound；合并后校验不过 → ErrInvalid。
func (s *ChannelStore) Update(id, typ, format, name string, enabled *bool, events []string, haveEvents bool, secrets map[string]string) (Channel, error) {
	var out Channel
	err := s.st.Update(func(f *channelsFile) error {
		for i := range f.Channels {
			if f.Channels[i].ID != id {
				continue
			}
			cur := f.Channels[i].clone()
			if err := applyUpdate(&cur, typ, format, name, enabled, events, haveEvents, secrets); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			f.Channels[i] = cur
			out = cur
			return nil
		}
		return ErrNotFound
	})
	if err != nil {
		return Channel{}, err
	}
	return out, nil
}

// Delete 删除渠道；返回是否删除（不存在为 false）。
func (s *ChannelStore) Delete(id string) (bool, error) {
	deleted := false
	err := s.st.Update(func(f *channelsFile) error {
		kept := f.Channels[:0]
		for _, c := range f.Channels {
			if c.ID == id {
				deleted = true
				continue
			}
			kept = append(kept, c)
		}
		f.Channels = kept
		return nil
	})
	return deleted, err
}

// ImportLegacy hub.yaml 旧 notify 段一次性导入（幂等；hub 专用——node 侧无
// hub.yaml 配置源，从不调用）：仅当「配置了 webhook_url」且「尚未导入（标记
// 未写）」时执行——渠道存储为空 → 导入为一个渠道；已有手工渠道 → 让位不覆盖。
// 两种结局都写 legacy_imported 标记（同事务落盘）：让位不写标记的话，用户日后
// 删光手工渠道+重启，旧 YAML 渠道会复活。返回本次是否实际导入。format 取
// feishu → webhook 渠道 + 飞书消息格式（2026-10-26 起直接落新形态，无独立
// feishu 类型可产）；其余（raw/空）→ 通用格式。任何一步失败不写标记，下次
// 启动重试。
func (s *ChannelStore) ImportLegacy(webhookURL, format string, now time.Time) (bool, error) {
	if strings.TrimSpace(webhookURL) == "" {
		return false, nil
	}
	chFormat := ""
	if format == FormatFeishu {
		chFormat = FormatFeishu
	}
	imported, yielded := false, false
	err := s.st.Update(func(f *channelsFile) error {
		if f.LegacyImported {
			return nil // 已导入：永不重导
		}
		if len(f.Channels) > 0 {
			// 手工渠道在先：永久让位——此刻即写标记，删光渠道也不会复活旧 YAML 配置
			f.LegacyImported = true
			yielded = true
			return nil
		}
		secrets := map[string]string{"url": strings.TrimSpace(webhookURL)}
		if err := validateChannel(TypeWebhook, chFormat, legacyChannelName, nil, secrets); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		id, err := newChannelID()
		if err != nil {
			return err
		}
		f.Channels = append(f.Channels, Channel{
			ID:        id,
			Type:      TypeWebhook,
			Format:    chFormat,
			Name:      legacyChannelName,
			Enabled:   true,
			Events:    nil, // 全订阅（旧配置语义：全部终态都通知）
			Secrets:   secrets,
			CreatedAt: now.UTC(),
		})
		f.LegacyImported = true // 导入与标记同事务落盘：崩溃恢复不重导
		imported = true
		return nil
	})
	if imported {
		log.Printf("[hub] 已从 hub.yaml notify 段导入通知渠道「%s」（type=webhook format=%s）；此后请改用管理台「设置 → 通知」，hub.yaml notify 段不再生效", legacyChannelName, formatLabel(chFormat))
	}
	if yielded {
		log.Printf("[hub] 检测到手工通知渠道：hub.yaml notify 段永久让位（此后不再导入，删光渠道也不会复活旧配置）")
	}
	return imported, err
}

// formatLabel 人话格式名（导入日志用）。
func formatLabel(f string) string {
	if f == FormatFeishu {
		return "feishu"
	}
	return "generic"
}

// legacyChannelName 兼容导入渠道的默认名称。
const legacyChannelName = "hub.yaml 导入"

// copySecrets 凭证映射拷贝（nil 安全）。
func copySecrets(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
