package conn

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// TLS 指纹固定 / TOFU 集成单测（SPEC 二）：真 TLS 握手 + 真注册握手——pin 强校验、
// trusted_hubs.json 首连记录、指纹变更拒绝。

// testCert 自签证书（ECDSA P-256）+ SPKI 指纹（base64）。
func testCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake-hub"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		FingerprintSPKI(parsed.RawSubjectPublicKeyInfo)
}

// fakeHubTLS 最小 hub：TLS + websocket.Accept + register→register_ack(ok, epoch=1)。
type fakeHubTLS struct {
	ln  net.Listener
	srv *http.Server
}

func newFakeHubTLS(t *testing.T, cert tls.Certificate) *fakeHubTLS {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHubTLS{ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/node", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, data, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			env, err := protocol.Decode(data)
			if err != nil || env.Type != protocol.MsgRegister {
				continue
			}
			ack, _ := json.Marshal(protocol.RegisterAckPayload{OK: true, Epoch: 1})
			ackEnv, _ := protocol.Encode(&protocol.Envelope{
				Version: protocol.ProtocolVersion, Type: protocol.MsgRegisterAck,
				MessageID: protocol.NewMessageID(), To: env.From, Payload: ack,
			})
			if err := ws.Write(context.Background(), websocket.MessageText, ackEnv); err != nil {
				return
			}
			for { // 注册成功后挂住读，保持会话直至对端关闭
				if _, _, err := ws.Read(context.Background()); err != nil {
					return
				}
			}
		}
	})
	h.srv = &http.Server{Handler: mux, TLSConfig: &tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
	}}
	go func() { _ = h.srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = h.srv.Close() })
	return h
}

// addr hub 地址（含 /ws/node 路径）。
func (h *fakeHubTLS) addr() string { return "wss://" + h.ln.Addr().String() + "/ws/node" }

// runClient 启动客户端，返回 OnRegistered 信号（注册成功一次即关闭信号）。
func runClient(t *testing.T, cfg Config) (registered chan struct{}, stop func()) {
	t.Helper()
	registered = make(chan struct{}, 8)
	cfg.OnRegistered = func() { registered <- struct{}{} }
	c := NewClient(cfg)
	done := make(chan struct{})
	go func() { _ = c.Run(context.Background()); close(done) }()
	return registered, func() {
		_ = c.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("Run 未在关闭后 5s 内退出")
		}
	}
}

func waitForReg(t *testing.T, ch chan struct{}, within time.Duration, want bool, what string) {
	t.Helper()
	select {
	case <-ch:
		if !want {
			t.Fatalf("%s：不应注册成功", what)
		}
	case <-time.After(within):
		if want {
			t.Fatalf("%s：%v 内未注册成功", what, within)
		}
	}
}

// TestTOFUFirstConnectRecords 首连成功 → trusted_hubs.json 记录 host:port→fp。
func TestTOFUFirstConnectRecords(t *testing.T) {
	cert, fp := testCert(t)
	hub := newFakeHubTLS(t, cert)
	ts := OpenTrustStore(t.TempDir())

	reg, stop := runClient(t, Config{
		HubURL: hub.addr(), NodeID: "n1", Token: "tok", TrustStore: ts,
		PingInterval: 100 * time.Millisecond,
	})
	waitForReg(t, reg, 5*time.Second, true, "TOFU 首连")
	stop()

	key, err := trustHostPort(hub.addr())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ts.Get(key)
	if !ok || got != fp {
		t.Fatalf("TOFU 记录不符: key=%s got=%s want=%s ok=%v", key, got, fp, ok)
	}
}

// TestTOFURejectsChangedCert 首连记录后，同地址换证书 → 握手拒绝，不注册成功。
func TestTOFURejectsChangedCert(t *testing.T) {
	cert1, _ := testCert(t)
	hub1 := newFakeHubTLS(t, cert1)
	ts := OpenTrustStore(t.TempDir())

	reg, stop := runClient(t, Config{
		HubURL: hub1.addr(), NodeID: "n1", Token: "tok", TrustStore: ts,
	})
	waitForReg(t, reg, 5*time.Second, true, "首连")
	stop()
	_ = hub1.srv.Close() // 释放连接资源

	// 新 hub（新证书）。为避免端口复用的平台差异，TOFU 键对应的信任记录直接
	// 预置为「另一指纹」——等价于「记录后 hub 换证书」，判定路径相同
	//（Get → 比对 → 拒绝），真正的首连写入已由 TestTOFUFirstConnectRecords 覆盖。
	cert2, _ := testCert(t)
	hub2 := newFakeHubTLS(t, cert2)
	key, err := trustHostPort(hub2.addr())
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Put(key, "Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm8="); err != nil {
		t.Fatal(err)
	}
	reg2, stop2 := runClient(t, Config{
		HubURL: hub2.addr(), NodeID: "n1", Token: "tok", TrustStore: ts,
	})
	waitForReg(t, reg2, 2*time.Second, false, "指纹变更后")
	stop2()
}

// TestPinMode 强校验模式：指纹匹配注册成功；不匹配握手失败（token 不出本机）。
func TestPinMode(t *testing.T) {
	cert, fp := testCert(t)
	hub := newFakeHubTLS(t, cert)

	reg, stop := runClient(t, Config{
		HubURL: hub.addr(), NodeID: "n1", Token: "tok", PubkeyFP: fp,
	})
	waitForReg(t, reg, 5*time.Second, true, "pin 匹配")
	stop()

	reg2, stop2 := runClient(t, Config{
		HubURL: hub.addr(), NodeID: "n1", Token: "SECRET-TOKEN", PubkeyFP: "Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm8=",
	})
	waitForReg(t, reg2, 2*time.Second, false, "pin 不匹配")
	stop2()
}

// TestTrustStorePersistence 落盘/重载往返（原子写 + 惰性加载）。
func TestTrustStorePersistence(t *testing.T) {
	dir := t.TempDir()
	ts := OpenTrustStore(dir)
	if err := ts.Put("127.0.0.1:8443", "fp-1"); err != nil {
		t.Fatal(err)
	}
	// 新实例（模拟重启）读回
	ts2 := OpenTrustStore(filepath.Join(dir))
	if got, ok := ts2.Get("127.0.0.1:8443"); !ok || got != "fp-1" {
		t.Fatalf("重载不符: got=%s ok=%v", got, ok)
	}
	if _, ok := ts2.Get("other:1"); ok {
		t.Fatal("未知键不应命中")
	}
}
