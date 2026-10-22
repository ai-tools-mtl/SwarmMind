// 接入身份白名单校验与接入串解析（方案 3.6 校验同源下沉，2026-10-22）：
// node_id / token / hub_url 三条白名单规则此前只在 hub enroll 一处实现，node
// setup 向导引入后若各自手写必然漂移——现下沉 protocol 包，hub（部署包渲染）
// 与 node（接入向导落盘）import 同源，规则改动一处生效。
//
// 本文件是纯协议层：只含校验与解析，不做 IO。
package protocol

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// 白名单正则（与 hub 接入向导前端校验对齐）：
//   - node_id：2-32 位小写字母/数字/连字符，首尾必须字母数字（禁连字符开头/
//     结尾、禁单字符）；二者会原样渲染进 node.yaml，超界即注入面；
//   - token：64 位小写十六进制（256bit 随机令牌的 hex 编码）。
var (
	nodeIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)
	tokenRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// 校验失败的人话原因（hub/node 同源文案，矩阵 #11 人话提示的统一出处）。
var (
	// ErrJoinNodeID node_id 不在白名单。
	ErrJoinNodeID = errors.New("node_id 须为 2-32 位小写字母/数字/连字符")
	// ErrJoinToken token 不在白名单。
	ErrJoinToken = errors.New("token 须为 64 位十六进制")
	// ErrJoinHubURL hub_url 不在白名单。
	ErrJoinHubURL = errors.New("hub_url 须为可解析的 ws:// 或 wss:// 地址")
	// ErrJoinFormat 接入串整体格式非法（非 swarmmind://join 或缺参数）。
	ErrJoinFormat = errors.New("接入串格式非法：须为 swarmmind://join?hub=…&node=…&token=…（从 hub 控制台接入向导复制）")
)

// ValidNodeID 报告 s 是否为合法 node_id（2-32 位小写字母/数字/连字符，首尾字母数字）。
func ValidNodeID(s string) bool { return nodeIDRe.MatchString(s) }

// ValidToken 报告 s 是否为合法接入令牌（64 位小写十六进制）。
func ValidToken(s string) bool { return tokenRe.MatchString(s) }

// ValidHubURL 报告 s 是否为合法 hub 外连地址：ws(s):// + host 非空 + 全串无
// 可打印 ASCII 之外的字符（控制字符/空格/引号/反斜杠一律拒——它是 yaml 纯量与
// Content-Disposition 的原料，超界即注入面）。
func ValidHubURL(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return false
		}
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "ws" || u.Scheme == "wss") && u.Host != ""
}

// JoinValues 接入三元组（解析/校验通过的产物）：hub 外连地址、node 身份、接入令牌。
type JoinValues struct {
	HubURL string
	NodeID string
	Token  string
}

// ParseJoinString 解析并校验接入串（方案 3.3/3.6）：格式
// `swarmmind://join?hub=<wss url>&node=<node_id>&token=<64hex>`，值 URL encode。
// 三元组逐项过白名单（与 enroll 渲染同源），非法返回人话错误（矩阵 #11）。
func ParseJoinString(raw string) (JoinValues, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "swarmmind" || u.Host != "join" || u.RawQuery == "" {
		return JoinValues{}, ErrJoinFormat
	}
	q := u.Query()
	v := JoinValues{HubURL: q.Get("hub"), NodeID: q.Get("node"), Token: q.Get("token")}
	if v.HubURL == "" || v.NodeID == "" || v.Token == "" {
		return JoinValues{}, fmt.Errorf("%w：hub / node / token 三个参数缺一不可", ErrJoinFormat)
	}
	switch {
	case !ValidNodeID(v.NodeID):
		return JoinValues{}, ErrJoinNodeID
	case !ValidToken(v.Token):
		return JoinValues{}, ErrJoinToken
	case !ValidHubURL(v.HubURL):
		return JoinValues{}, ErrJoinHubURL
	}
	return v, nil
}
