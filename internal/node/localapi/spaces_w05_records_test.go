package localapi

// spaces_w05_records_test.go 策略面 REST 测试（W05-04）：records 四族登记（kind 词
// 表/幂等码/越权 403/悬空锚 404/自报不证实 400/终态拒收）、records 增量读游标、
// strategy 投影端点（如实空态不编造）。机械全链（外部管家驱动讨论/写审两策略 +
// AC12/13/17 断言）见 spaces_w05_mech_test.go。

import (
	"net/http"
	"strings"
	"testing"
)

// TestSpaceRecordsSurface records 面契约：kind 词表/非法词形 400、candidate 越权
// 403 零登记、FINAL 词形门、版本登记幂等 200、证据自报 400、issue 悬空锚 404、
// 终态空间拒收、records since 游标、strategy 空间不存在 404。
func TestSpaceRecordsSurface(t *testing.T) {
	srv, eng := newStrategySpaceSrv(t)
	tok := "tok-local"
	code, out := spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g","mode":"manual","acceptance":"验收","members":[{"name":"alice","role":"writer"},{"name":"bob","role":"reviewer"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("建空间应 201: %d %s", code, out)
	}
	id := gjsonStr2(t, out, "space.id")

	// kind 词表：未知 kind → 400（词表如实全列）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records", `{"kind":"vibe"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, "candidate|review|issue|evidence") {
		t.Fatalf("未知 kind 应 400 且列词表: %d %s", code, out)
	}

	// candidate：非法词形（缺 FINAL 前缀）400；越权提出者 403（零登记）；合法登记 201。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"candidate","member":"alice","body":"普通发言"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, "FINAL") {
		t.Fatalf("非 FINAL 候选应 400: %d %s", code, out)
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"candidate","member":"ghost","body":"FINAL: 越权"}`)
	if code != http.StatusForbidden || !strings.Contains(out, "proposer_not_member") {
		t.Fatalf("越权提出者应 403: %d %s", code, out)
	}
	if cands, _ := eng.RecordsSince(id, 0); len(cands) != 0 {
		t.Fatalf("越权拒绝须零登记: %d", len(cands))
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"candidate","member":"alice","body":"FINAL: 方案一"}`)
	if code != http.StatusCreated || !strings.Contains(out, `"created":true`) {
		t.Fatalf("合法候选应 201: %d %s", code, out)
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"candidate","member":"alice","body":"FINAL: 方案一"}`)
	if code != http.StatusOK || !strings.Contains(out, `"created":false`) {
		t.Fatalf("同文候选应 200 命中既有: %d %s", code, out)
	}

	// review/revision：参数非法 400（hash 词形）；合法 201；同内容幂等 200。
	hash := strings.Repeat("ab", 32)
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"review","op":"revision","baseline":"main","content_sha256":"nothex","author":"alice"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, "revision_invalid") {
		t.Fatalf("hash 词形非法应 400: %d %s", code, out)
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"review","op":"revision","baseline":"main","content_sha256":"`+hash+`","author":"alice","author_round":1}`)
	if code != http.StatusCreated {
		t.Fatalf("版本登记应 201: %d %s", code, out)
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"review","op":"revision","baseline":"main","content_sha256":"`+hash+`","author":"alice","author_round":1}`)
	if code != http.StatusOK || !strings.Contains(out, `"created":false`) {
		t.Fatalf("同内容版本应 200 幂等: %d %s", code, out)
	}

	// evidence：自报标证实 400（永不可证实）；未知层 400；回执来源 201。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"evidence","version":"`+hash+`","layer":"cli_exit","source":"agent_self_report","verified":true,"ref":"self"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, "自报") {
		t.Fatalf("自报标证实应 400: %d %s", code, out)
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"evidence","version":"`+hash+`","layer":"vibes","source":"executor_receipt","verified":true,"ref":"r"}`)
	if code != http.StatusBadRequest || !strings.Contains(out, "evidence_invalid") {
		t.Fatalf("未知证据层应 400: %d %s", code, out)
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"evidence","version":"`+hash+`","layer":"cli_exit","source":"executor_receipt","verified":true,"ref":"run-1"}`)
	if code != http.StatusCreated {
		t.Fatalf("回执证据应 201: %d %s", code, out)
	}

	// issue：发现版本悬空锚 404；合法 201。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"issue","reviewer":"bob","severity":"must","file":"a.go","line":1,"title":"t","found_in_version":"`+strings.Repeat("cd", 32)+`"}`)
	if code != http.StatusNotFound || !strings.Contains(out, "revision_not_found") {
		t.Fatalf("悬空发现版本应 404: %d %s", code, out)
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces/"+id+"/records",
		`{"kind":"issue","reviewer":"bob","severity":"must","file":"a.go","line":1,"title":"边界缺失","found_in_version":"`+hash+`"}`)
	if code != http.StatusCreated || !strings.Contains(out, `"id":"iss-`) {
		t.Fatalf("问题登记应 201: %d %s", code, out)
	}

	// records 增量读：升序 + since 游标 + type 四族标注。
	code, out = spaceDo(t, srv.Server, tok, http.MethodGet, "/api/v1/spaces/"+id+"/records", "")
	if code != http.StatusOK || !strings.Contains(out, `"type":"candidate"`) || !strings.Contains(out, `"type":"review"`) ||
		!strings.Contains(out, `"type":"issue"`) || !strings.Contains(out, `"type":"evidence"`) {
		t.Fatalf("records 读应含四族标注: %d %s", code, out)
	}
	// since 负值/非数字 → 400。
	for _, bad := range []string{"since=-1", "since=x"} {
		code, out = spaceDo(t, srv.Server, tok, http.MethodGet, "/api/v1/spaces/"+id+"/records?"+bad, "")
		if code != http.StatusBadRequest {
			t.Fatalf("非法 since 应 400: %d %s", code, out)
		}
	}

	// strategy 投影端点：200 + has_loop；空间不存在 404。
	code, out = spaceDo(t, srv.Server, tok, http.MethodGet, "/api/v1/spaces/"+id+"/strategy", "")
	if code != http.StatusOK || !strings.Contains(out, `"has_loop":true`) {
		t.Fatalf("strategy 投影应 200: %d %s", code, out)
	}
	code, out = spaceDo(t, srv.Server, tok, http.MethodGet, "/api/v1/spaces/sp-x/strategy", "")
	if code != http.StatusNotFound {
		t.Fatalf("投影空间不存在应 404: %d %s", code, out)
	}
	// 无事实空间：如实空态（has_loop=false）。
	code, out = spaceDo(t, srv.Server, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g2","mode":"manual","members":[{"name":"alice"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("第二空间应 201: %d %s", code, out)
	}
	id2 := gjsonStr2(t, out, "space.id")
	code, out = spaceDo(t, srv.Server, tok, http.MethodGet, "/api/v1/spaces/"+id2+"/strategy", "")
	if code != http.StatusOK || !strings.Contains(out, `"has_loop":false`) {
		t.Fatalf("无事实空间应 has_loop=false 如实空态: %d %s", code, out)
	}

	// 编排主体登记（orchestrate 可登记非凭据记录）与终态拒收。
	srv3, _ := newStrategySpaceSrv(t)
	code, out = spaceDo(t, srv3.Server, tok, http.MethodPost, "/api/v1/spaces",
		`{"goal":"g3","mode":"manual","acceptance":"a","policy":{"finish_auth":"human_only"},"members":[{"name":"alice"}]}`)
	id3 := gjsonStr2(t, out, "space.id")
	code, out = spaceDo(t, srv3.Server, "tok-prime", http.MethodPost, "/api/v1/spaces/"+id3+"/records",
		`{"kind":"candidate","member":"alice","body":"FINAL: 编排主体登记"}`)
	if code != http.StatusCreated {
		t.Fatalf("编排主体登记应放行（authSpaceSubject 同门）: %d %s", code, out)
	}
	// 终态拒收：abandoned 定版后 records 409。
	for _, spec := range []struct{ act, body string }{
		{"ready", `{"request_id":"rt-r","action":"ready","expected_version":1}`},
		{"start", `{"request_id":"rt-s","action":"start","expected_version":2}`},
		// start 后循环引导 drafting 落账占一版：现版本 4。
		{"finalize", `{"request_id":"rt-f","action":"finalize","expected_version":4,"final_outcome":"abandoned","reason":"需求放弃"}`},
	} {
		code, out = spaceDo(t, srv3.Server, tok, http.MethodPost, "/api/v1/spaces/"+id3+"/control", spec.body)
		if code != http.StatusOK {
			t.Fatalf("%s 应 200: %d %s", spec.act, code, out)
		}
	}
	code, out = spaceDo(t, srv3.Server, tok, http.MethodPost, "/api/v1/spaces/"+id3+"/records",
		`{"kind":"candidate","member":"alice","body":"FINAL: 终态后登记"}`)
	if code != http.StatusConflict || !strings.Contains(out, "illegal_transition") {
		t.Fatalf("终态空间 records 应 409 拒收: %d %s", code, out)
	}
}
