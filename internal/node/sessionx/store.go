package sessionx

// sessions.json 持久化（TASK-agent-session-core 改动范围 2）：格式参照
// api_tokens.json 惯例——JSON、原子写（独占创建 tmp + 文件 fsync + rename，目录
// fsync 尽力而为，承诺边界见 saveSessionsLocked）、0600；损坏/缺失按空表起步并
// 如实记日志。落盘内容含平台↔原生 ID 映射与轮次留痕（含失败轮，R1 审查 R1）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// sessionsVersion sessions.json 结构版本（未来迁移依据）。
const sessionsVersion = 1

// persistedSessions 磁盘形态。
type persistedSessions struct {
	Version  int                `json:"version"`
	Sessions []protocol.Session `json:"sessions"`
	// Closing 关闭墓碑（T2-R1 必修4）：映射发布前收到关闭意图的平台会话 ID 集
	//（排序稳定）。创建发布前核验，命中则拒绝创建（ErrSessionClosed）——关闭
	// 决定跨重启留存，「关闭成功后会话复活」无持久化缺口。可加性字段：旧文件
	// 缺失即空表，无需迁移。
	Closing []string `json:"closing,omitempty"`
}

// loadSessions 装载磁盘会话表并登记写者锁句柄。仅启动期（NewManager）调用，
// 尚未共享，无锁要求。损坏文件按空表起步：内核不阻断启动，日志如实记录，
// 下次落盘整体重写。
func (m *Manager) loadSessions() {
	b, err := os.ReadFile(m.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			m.log.Printf("sessionx: sessions.json 读取失败（%v），按空表起步", err)
		}
		return // 缺失 = 首次运行，正常
	}
	var p persistedSessions
	if err := json.Unmarshal(b, &p); err != nil {
		m.log.Printf("sessionx: sessions.json 损坏（%v），按空表起步，下次落盘整体重写", err)
		return
	}
	for i := range p.Sessions {
		sess := p.Sessions[i]
		if sess.ID == "" {
			continue // 空条目防御
		}
		if _, dup := m.sessions[sess.ID]; dup {
			continue // 重复 ID 防御：保留先到
		}
		m.sessions[sess.ID] = &sess
		m.writers[sess.ID] = &sessionGate{}
	}
	for _, id := range p.Closing {
		if id == "" {
			continue // 空条目防御
		}
		m.closing[id] = true
	}
}

// saveSessionsLocked 全量原子落盘（R1 审查 S3 修订、R2 复审 S3 承诺收窄：文案与
// 实现一致）。步骤：独占创建 0600 临时文件（O_EXCL——绝不复用磁盘上可能带旧权限/
// 旧内容的残留 tmp），写全量快照，fsync 文件，rename 提交，最后尽力 fsync 目录。
// 承诺边界：
//   - 原子替换：tmp+rename 保证读者只见旧完整或新完整，绝无半截 JSON；
//   - 文件内容先于改名落盘：进程崩溃（非掉电）场景新内容不丢、不回退到旧内容；
//   - 无掉电耐久声明：目录 fsync 尽力而为（Unix 失败、Windows 侧不支持时只记
//     日志不报错），极端掉电下「rename 已可见而目录项未持久」的回退窗口不在担保内；
//   - rename 成功即提交：此后无错误路径、不回退旧内容；rename 之前的任一步失败：
//     清理 tmp、返回错误，磁盘旧正本原样保留
//     （TestSessionsJSONRenameFailureCleansTmpKeepsTarget 断言）。
//
// 调用方须持 m.mu——单文件单写者，杜绝并发写交错。全量快照按创建时刻升序
// （同刻按 ID），字节序确定。
func (m *Manager) saveSessionsLocked() error {
	list := make([]protocol.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
	closing := make([]string, 0, len(m.closing))
	for id := range m.closing {
		closing = append(closing, id)
	}
	sort.Strings(closing)
	b, err := json.MarshalIndent(persistedSessions{Version: sessionsVersion, Sessions: list, Closing: closing}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := m.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// 上次落盘崩溃残留的 tmp：清掉重试一次（磁盘旧正本未动，安全）。
		if rmErr := os.Remove(tmp); rmErr != nil {
			return fmt.Errorf("清理残留临时文件失败: %w", rmErr)
		}
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil { // 文件内容落盘先于 rename（进程崩溃不丢不回退；无掉电耐久承诺见函数头）
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		os.Remove(tmp) // rename 失败旧数据原样保留，不留半截 tmp
		return err
	}
	// 全量快照已确认落盘（T2-R2 N2）：dirty 至此清零——此后关闭类路径的幂等
	// 快速返回有磁盘背书；失败路径不清零，下次关闭请求继续重试并如实报错。
	m.persistDirty = false
	// 目录 fsync 尽力而为（R2 复审 S3：不承担掉电耐久承诺）。rename 已成功即提交，
	// 此刻报「持久化失败」会教调用方误信磁盘没有数据（从而依赖内存态、等待下次
	// 重写），与事实相反——任何目录打开/同步失败只记日志，不改变提交结果。
	if d, err := os.Open(filepath.Dir(m.path)); err != nil {
		m.log.Printf("sessionx: sessions.json 目录打开失败（%v），目录同步跳过：原子替换已提交，无掉电耐久担保", err)
	} else {
		if serr := d.Sync(); serr != nil {
			m.log.Printf("sessionx: sessions.json 目录同步未生效（%v）：原子替换已提交，无掉电耐久担保", serr)
		}
		d.Close()
	}
	return nil
}
