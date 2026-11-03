package main

// W09 收尾加固 A 装配锚（构造级，不起真进程）：hub 侧 newStrategyEngine 与 node
// 同态同形——「文件 → 引擎」恢复链：缺文件零操作空态、变更即落盘（version=1、
// 0600）、新引擎读回恢复、损坏拒绝启动（同 spaces.json 宁拒启口径）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/strategy"
)

func TestNewStrategyEnginePersistAssembly(t *testing.T) {
	dir := t.TempDir()

	// 缺文件 = 零操作（老部署/首次启动，如实空态）。
	eng, err := newStrategyEngine(dir)
	if err != nil {
		t.Fatalf("缺文件应零操作启动: %v", err)
	}
	if ids := eng.LoopIDs(); len(ids) != 0 {
		t.Fatalf("缺文件应空态: %v", ids)
	}

	// 变更即落盘：登记候选 → strategy_loops.json 在位（version=1、0600）。
	if _, _, err := eng.RegisterCandidate("sp-hub", strategy.CandidateInput{
		Proposer: "alice", Body: "FINAL: 采用方案一", Round: 1,
	}); err != nil {
		t.Fatalf("RegisterCandidate: %v", err)
	}
	path := filepath.Join(dir, strategy.LoopStoreFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("变更后持久化文件缺失: %v", err)
	}
	var doc struct {
		Version int `json:"version"`
		Loops   []struct {
			SpaceID string `json:"space_id"`
		} `json:"loops"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("文件应合法 JSON: %v", err)
	}
	if doc.Version != 1 || len(doc.Loops) != 1 || doc.Loops[0].SpaceID != "sp-hub" {
		t.Fatalf("文件形态应 {version:1,loops:[sp-hub]}: %+v", doc)
	}
	if fi, serr := os.Stat(path); serr != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("文件权限应 0600: %v %v", fi, serr)
	}

	// 文件 → 引擎恢复链：新引擎读回（重启等价）。
	eng2, err := newStrategyEngine(dir)
	if err != nil {
		t.Fatalf("恢复启动: %v", err)
	}
	if ids := eng2.LoopIDs(); len(ids) != 1 || ids[0] != "sp-hub" {
		t.Fatalf("恢复后 LoopIDs 应 [sp-hub]: %v", ids)
	}
	if got := eng2.PendingCandidates("sp-hub"); len(got) != 1 || got[0].FinalText != "FINAL: 采用方案一" {
		t.Fatalf("恢复链断点：候选应读回 1 条: %+v", got)
	}

	// 损坏拒绝启动（宁拒启口径）。
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := newStrategyEngine(dir)
	if err == nil || e != nil {
		t.Fatalf("损坏文件应拒绝启动: err=%v eng=%v", err, e)
	}
	if !strings.Contains(err.Error(), "损坏") {
		t.Fatalf("错误信息应指明损坏: %v", err)
	}
}
