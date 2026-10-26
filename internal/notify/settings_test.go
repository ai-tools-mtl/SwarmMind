// 「同时通知」开关读写测试（codex 审查必修 1，2026-10-26）：写路径 per-path
// 串行化——settings.Open 每次新开 Store、内部互斥锁跨调用不生效，无串行化时
// 并发 SetAlsoNotify 共用的固定 .tmp 临时文件互相截断，rename 可能把半截 JSON
// 发布成正式文件。
package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestSetAlsoNotifyConcurrent 并发写串行化：64 goroutine × 25 次交替写同一
// data_dir，结束后正式文件必须是合法 JSON 且 also_notify 为显式布尔——.tmp
// 互踩的截断/混写残留会在这里炸出来；配合 go test -race 验证锁覆盖完整
//（写事务全程持锁，无数据竞争）。最终值 true/false 均合法（最后完成的写者
// 赢，时序不定），不做值断言。
func TestSetAlsoNotifyConcurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	for w := 0; w < 64; w++ {
		v := w%2 == 0
		wg.Add(1)
		go func(v bool) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if err := SetAlsoNotify(dir, v); err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
			}
		}(v)
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatalf("并发写失败: %v", err)
	default:
	}
	b, err := os.ReadFile(filepath.Join(dir, "notify_settings.json"))
	if err != nil {
		t.Fatalf("并发写后读正式文件失败: %v", err)
	}
	var f SettingsFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("并发写后正式文件损坏（.tmp 互踩痕迹）: %v\nraw: %s", err, b)
	}
	if f.AlsoNotify == nil {
		t.Fatalf("also_notify 应为显式布尔，got: %s", b)
	}
}
