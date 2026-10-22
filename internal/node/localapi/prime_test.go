package localapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mtl802/swarmmind/internal/node/ledger"
	svcpkg "github.com/mtl802/swarmmind/internal/node/service"
	"github.com/mtl802/swarmmind/internal/protocol"
	"github.com/mtl802/swarmmind/internal/transport"
)

// 总理凭证单测（方案 3.2.1）：编排门 403 四分支按优先级可区分（复审 #8）；轮换
// 即时失效旧凭证、新凭证可编排；rotate 端点仅 local_token 可调。

// echoStubRunner 恒成功桩执行器（localapi 包内本地直执行用）。
type echoStubRunner struct{}

func (echoStubRunner) Run(_ context.Context, _ protocol.Task) protocol.TaskResult {
	return protocol.TaskResult{Status: protocol.ResultCompleted, FinalAnswer: "ok"}
}

// doJSON 带 Bearer 的 JSON 请求便捷封装，返回状态码与应答体。
func doJSON(t *testing.T, srv *httptest.Server, token, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestAuthPrime403Priority 编排门 403 四分支（server.go authPrime，先认证后鉴权、
// 拒绝理由按优先级给出）：setup 态 > 未钦定总理 > 总理凭证未配置 > 级别不足，
// 各自人话信息可区分。四个分支均在编排门内拒绝（Service 可为 nil）。
func TestAuthPrime403Priority(t *testing.T) {
	cases := []struct {
		name  string
		cfg   Config
		token string
		want  string
	}{
		{
			name:  "setup态编排未激活",
			cfg:   Config{NodeID: "nodeA", Setup: true, PrimeAgent: "echo", PrimeToken: "tok-prime", LocalToken: "tok-local"},
			token: "tok-prime",
			want:  "setup 模式编排接口未激活",
		},
		{
			name:  "未钦定总理",
			cfg:   Config{NodeID: "nodeA", PrimeAgent: "", PrimeToken: "tok-prime", LocalToken: "tok-local"},
			token: "tok-prime",
			want:  "尚未钦定总理",
		},
		{
			name:  "总理凭证未配置",
			cfg:   Config{NodeID: "nodeA", PrimeAgent: "echo", PrimeToken: "", LocalToken: "tok-local"},
			token: "tok-local", // local_token 过认证（levelRead），命中 PrimeToken 空分支
			want:  "总理凭证未配置",
		},
		{
			name:  "凭证级别不足",
			cfg:   Config{NodeID: "nodeA", PrimeAgent: "echo", PrimeToken: "tok-prime", LocalToken: "tok-local"},
			token: "tok-local",
			want:  "编排操作需总理凭证",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(New(tc.cfg).Handler())
			t.Cleanup(srv.Close)
			code, out := doJSON(t, srv, tc.token, http.MethodPost, "/api/v1/tasks",
				`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
			if code != http.StatusForbidden {
				t.Fatalf("应 403, got %d: %s", code, out)
			}
			if !strings.Contains(out, "prime_required") || !strings.Contains(out, tc.want) {
				t.Fatalf("错误信息应含可区分片段 %q: %s", tc.want, out)
			}
		})
	}
}

// TestPrimeRotateInvalidatesOldToken 轮换失效（复审 #8）：rotate 后旧 prime_token
// 调编排被拒——旧凭证已不属于任何已知令牌，按 authPrime 先认证后鉴权的次序在
// 认证层 401（403 语义是「已认证但级别不足」，与 prime.go 注释「401/403」一致）；
// 新凭证 202；rotate 端点仅 local_token 可调（prime_token → 401）。
func TestPrimeRotateInvalidatesOldToken(t *testing.T) {
	bus := transport.NewBus()
	led, _, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	svc := svcpkg.New(svcpkg.Config{
		Ledger:  led,
		Runners: map[string]svcpkg.Runner{"echo": echoStubRunner{}},
	}, bus.Endpoint("nodeA"))
	t.Cleanup(svc.Close)
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc, Ledger: led,
		LocalToken: "tok-local", PrimeToken: "tok-old", PrimeAgent: "echo",
		RotatePrime: func() (string, error) { return "tok-new", nil },
	}).Handler())
	t.Cleanup(srv.Close)
	submit := func(token string) (int, string) {
		return doJSON(t, srv, token, http.MethodPost, "/api/v1/tasks",
			`{"target_node":"nodeA","target_agent":"echo","submitted_by":"p"}`)
	}

	// 前置：轮换前旧凭证可编排
	if code, out := submit("tok-old"); code != http.StatusAccepted {
		t.Fatalf("轮换前旧凭证应 202, got %d: %s", code, out)
	}
	// rotate 端点仅 local_token 可调：prime_token → 401（本机主人操作不随编排凭证下发）
	if code, out := doJSON(t, srv, "tok-old", http.MethodPost, "/api/v1/local/prime/rotate", ""); code != http.StatusUnauthorized {
		t.Fatalf("prime_token 调 rotate 应 401, got %d: %s", code, out)
	}
	code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/rotate", "")
	if code != http.StatusOK {
		t.Fatalf("local_token 调 rotate 应 200, got %d: %s", code, out)
	}
	var body struct {
		PrimeToken string `json:"prime_token"`
		PrimeAgent string `json:"prime_agent"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil || body.PrimeToken != "tok-new" || body.PrimeAgent != "echo" {
		t.Fatalf("rotate 应返回新凭证（仅此一次展示）: %s", out)
	}
	// 轮换后：旧凭证被拒（认证层 401）、新凭证 202
	if code, out := submit("tok-old"); code != http.StatusUnauthorized {
		t.Fatalf("轮换后旧凭证调编排应被拒 401, got %d: %s", code, out)
	}
	if code, out := submit("tok-new"); code != http.StatusAccepted {
		t.Fatalf("轮换后新凭证应 202, got %d: %s", code, out)
	}
}

// TestPrimeRotateSerializes 并发轮换（返工 #4 收口）：生成/写盘/内存重载同一
// muPrime 互斥段——并发轮换后内存凭证与最后一次写盘一致，且新旧凭证恰其一可用。
func TestPrimeRotateSerializes(t *testing.T) {
	n := 0
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", LocalToken: "tok-local",
		RotatePrime: func() (string, error) {
			n++
			return "tok-rot-" + string(rune('a'+n-1)), nil
		},
	}).Handler())
	t.Cleanup(srv.Close)
	tokens := make([]string, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, out := doJSON(t, srv, "tok-local", http.MethodPost, "/api/v1/local/prime/rotate", "")
			if code != http.StatusOK {
				t.Errorf("并发轮换应 200, got %d: %s", code, out)
				return
			}
			var body struct {
				PrimeToken string `json:"prime_token"`
			}
			if err := json.Unmarshal([]byte(out), &body); err != nil {
				t.Errorf("应答解析失败: %v", err)
				return
			}
			tokens[i] = body.PrimeToken
		}(i)
	}
	wg.Wait()
	// 4 次轮换各得唯一新凭证（tok-rot-a..d 各出现一次——互斥段串行化生成）
	seen := map[string]bool{}
	for _, tok := range tokens {
		if tok == "" || seen[tok] {
			t.Fatalf("并发轮换应各得唯一凭证, got %q", tokens)
		}
		seen[tok] = true
	}
}
