package settings

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type demoCfg struct {
	Items  []string          `json:"items"`
	Flags  map[string]string `json:"flags"`
	Number int               `json:"number"`
}

// TestOpenMissingFileStartsEmpty 文件不存在（首次启动）从零值起步。
func TestOpenMissingFileStartsEmpty(t *testing.T) {
	s, err := Open[demoCfg](t.TempDir(), "demo.json")
	if err != nil {
		t.Fatalf("打开: %v", err)
	}
	v := s.View()
	if len(v.Items) != 0 || v.Number != 0 {
		t.Errorf("首次启动应为零值: %+v", v)
	}
}

// TestOpenCorruptFileFails 配置损坏报错不静默清零（与 group 快照同口径）。
func TestOpenCorruptFileFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open[demoCfg](dir, "demo.json"); err == nil {
		t.Fatal("损坏文件应报错")
	}
}

// TestUpdatePersistsAndReloads 更新落盘后重开存储可恢复（原子写 + 持久化）。
func TestUpdatePersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open[demoCfg](dir, "demo.json")
	err := s.Update(func(v *demoCfg) error {
		v.Items = []string{"a", "b"}
		v.Flags = map[string]string{"k": "v"}
		v.Number = 7
		return nil
	})
	if err != nil {
		t.Fatalf("更新: %v", err)
	}
	s2, err := Open[demoCfg](dir, "demo.json")
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	v := s2.View()
	if v.Number != 7 || len(v.Items) != 2 || v.Flags["k"] != "v" {
		t.Errorf("重开恢复不符: %+v", v)
	}
	// 临时文件不残留（temp+rename 原子替换）
	if _, err := os.Stat(filepath.Join(dir, "demo.json.tmp")); err == nil {
		t.Error("临时文件不得残留")
	}
}

// TestViewReturnsIsolatedCopy View/Update 副本隔离：改副本不泄漏进存储状态。
func TestViewReturnsIsolatedCopy(t *testing.T) {
	s, _ := Open[demoCfg](t.TempDir(), "demo.json")
	if err := s.Update(func(v *demoCfg) error { v.Items = []string{"a"}; return nil }); err != nil {
		t.Fatal(err)
	}
	v := s.View()
	v.Items[0] = "篡改"
	v.Items = append(v.Items, "extra")
	v2 := s.View()
	if len(v2.Items) != 1 || v2.Items[0] != "a" {
		t.Errorf("副本改动不得泄漏: %+v", v2)
	}
	// Update 的 mutate 副本同理：mutate 内改坏了但返回错误 → 存储零变更
	if err := s.Update(func(v *demoCfg) error { v.Items = append(v.Items, "dirty"); return errFail }); err == nil {
		t.Fatal("mutate 报错应上抛")
	}
	if v3 := s.View(); len(v3.Items) != 1 {
		t.Errorf("失败事务零变更: %+v", v3)
	}
}

var errFail = &failErr{}

type failErr struct{}

func (*failErr) Error() string { return "fail" }

// TestConcurrentUpdates 并发 Update 串行化（-race 观察点）：全部落盘且计数正确。
func TestConcurrentUpdates(t *testing.T) {
	s, _ := Open[demoCfg](t.TempDir(), "demo.json")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Update(func(v *demoCfg) error { v.Number++; return nil })
		}()
	}
	wg.Wait()
	if n := s.View().Number; n != 50 {
		t.Errorf("并发更新应全部生效, got %d", n)
	}
}
