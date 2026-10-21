package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// scanArtifacts 产物扫描（SPEC 4.8/4.9）：进程结束后冻结产物清单——递归扫描
// workdir/outputs/<task_id>/，跳过符号链接，每个文件登记为 Artifact（相对 workdir
// 路径 + SHA-256 + 大小）。约束：≤ MaxArtifacts 条、单文件 ≤ ArtifactLimit，
// 超限跳过并以返回值注记；目录不存在则 Artifacts 为空。
// 登记后新增文件不追认（清单即冻结）；扫描顺序排序保证确定性。
func scanArtifacts(workdir, taskID string) ([]protocol.Artifact, int) {
	root := filepath.Join(workdir, "outputs", taskID)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, 0
	}

	type cand struct {
		rel  string
		path string
		size int64
	}
	var candidates []cand
	skipped := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 不可读条目跳过（进程产物可被并发清理，不致命）
		}
		if d.Type()&fs.ModeSymlink != 0 {
			skipped++ // 跳过符号链接（不跟随，防越界）
			return nil
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			skipped++
			return nil
		}
		rel, err := filepath.Rel(workdir, path)
		if err != nil {
			skipped++
			return nil
		}
		candidates = append(candidates, cand{rel: filepath.ToSlash(rel), path: path, size: fi.Size()})
		return nil
	})
	if err != nil {
		return nil, skipped
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].rel < candidates[j].rel })

	arts := make([]protocol.Artifact, 0, protocol.MaxArtifacts)
	for _, c := range candidates {
		if len(arts) >= protocol.MaxArtifacts {
			skipped++ // 条数超限：跳过并注记
			continue
		}
		if c.size > protocol.ArtifactLimit {
			skipped++ // 单文件超 640KiB：跳过并注记
			continue
		}
		sum, err := sha256File(c.path)
		if err != nil {
			skipped++
			continue
		}
		arts = append(arts, protocol.Artifact{Path: c.rel, Sha256: sum, Size: c.size})
	}
	if len(arts) == 0 {
		return nil, skipped
	}
	return arts, skipped
}

// sha256File 计算文件内容 SHA-256（hex）。
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开产物失败: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("读取产物失败: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
