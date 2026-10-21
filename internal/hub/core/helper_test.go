package core

import (
	"encoding/json"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/store"
)

// mustMarshal 测试辅助：json.Marshal 失败即致命。
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// jsonUnmarshal 测试辅助。
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// jsonMarshal 测试辅助：失败返回错误供调用方 t.Fatal。
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// storeOpen 测试辅助：打开 hub store（统一入口，便于 fullchain 语义清晰）。
func storeOpen(dir string) (*store.Store, error) {
	return store.Open(dir)
}
