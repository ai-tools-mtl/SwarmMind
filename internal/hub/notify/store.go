// 渠道存储：data_dir/notify_channels.json，经 settings.Store 原子读写（temp+
// rename），运行时可改——Dispatcher 每次终态事件都取最新快照，控制台增删改即
// 时生效，无需重启。
//
// hub.yaml 旧 notify 段向后兼容：启动时若配置了 webhook_url 且尚未导入 →
// 渠道存储为空则导入为一个 feishu/raw 渠道，已有手工渠道则让位；两种结局都写
// legacy_imported 标记（含让位——否则删光渠道+重启会复活旧 YAML 渠道），此后
// 无论渠道被改成什么样（含全部删除）都不再重导——「只导入一次」。
package notify

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/hub/settings"
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

// OpenChannelStore 打开 data_dir 下的渠道存储；文件不存在则从空表起步。
func OpenChannelStore(dataDir string) (*ChannelStore, error) {
	st, err := settings.Open[channelsFile](dataDir, "notify_channels.json")
	if err != nil {
		return nil, err
	}
	return &ChannelStore{st: st}, nil
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

// Create 校验并创建渠道；ID 自生成（c- 前缀），创建时间取 now。
func (s *ChannelStore) Create(typ, name string, events []string, secrets map[string]string, now time.Time) (Channel, error) {
	if err := validateChannel(typ, name, events, secrets); err != nil {
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

// Update 渠道更新（PUT 语义：占位保留凭证，见 applyUpdate）。id 不存在 →
// ErrNotFound；合并后校验不过 → ErrInvalid。
func (s *ChannelStore) Update(id, typ, name string, enabled *bool, events []string, haveEvents bool, secrets map[string]string) (Channel, error) {
	var out Channel
	err := s.st.Update(func(f *channelsFile) error {
		for i := range f.Channels {
			if f.Channels[i].ID != id {
				continue
			}
			cur := f.Channels[i].clone()
			if err := applyUpdate(&cur, typ, name, enabled, events, haveEvents, secrets); err != nil {
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

// ImportLegacy hub.yaml 旧 notify 段一次性导入（幂等）：仅当「配置了
// webhook_url」且「尚未导入（标记未写）」时执行——渠道存储为空 → 导入为一个
// 渠道；已有手工渠道 → 让位不覆盖。两种结局都写 legacy_imported 标记（同事务
// 落盘）：让位不写标记的话，用户日后删光手工渠道+重启，旧 YAML 渠道会复活。
// 返回本次是否实际导入。format 取 feishu → 渠道类型 feishu；其余（raw/空）→
// 通用 webhook 渠道（载荷同 raw）。任何一步失败不写标记，下次启动重试。
func (s *ChannelStore) ImportLegacy(webhookURL, format string, now time.Time) (bool, error) {
	if strings.TrimSpace(webhookURL) == "" {
		return false, nil
	}
	typ := TypeWebhook
	secretKey := "url"
	if format == FormatFeishu {
		typ = TypeFeishu
		secretKey = "webhook_url"
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
		secrets := map[string]string{secretKey: strings.TrimSpace(webhookURL)}
		if err := validateChannel(typ, legacyChannelName, nil, secrets); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		id, err := newChannelID()
		if err != nil {
			return err
		}
		f.Channels = append(f.Channels, Channel{
			ID:        id,
			Type:      typ,
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
		log.Printf("[hub] 已从 hub.yaml notify 段导入通知渠道「%s」（type=%s）；此后请改用管理台「设置 → 通知」，hub.yaml notify 段不再生效", legacyChannelName, typ)
	}
	if yielded {
		log.Printf("[hub] 检测到手工通知渠道：hub.yaml notify 段永久让位（此后不再导入，删光渠道也不会复活旧配置）")
	}
	return imported, err
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
