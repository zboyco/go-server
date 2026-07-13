package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/filter"
)

// result 记录单个演示的执行结果。
type result struct {
	name       string
	err        error
	skipReason string
}

// demoT 提供与 testing.T 类似的失败收集能力，便于在 main 中复用断言风格。
type demoT struct {
	name       string
	errors     []string
	skipReason string
	cleanups   []func()
}

// Name 返回当前演示名称。
func (t *demoT) Name() string { return t.name }

// Helper 对齐 testing.TB 接口，无实际逻辑。
func (t *demoT) Helper() {}

// Fatalf 记录错误并标记失败。
func (t *demoT) Fatalf(format string, args ...interface{}) {
	t.errors = append(t.errors, fmt.Sprintf(format, args...))
	panic(demoFailure{})
}

// Errorf 记录非致命错误。
func (t *demoT) Errorf(format string, args ...interface{}) {
	t.errors = append(t.errors, fmt.Sprintf(format, args...))
}

// Skipf 记录当前环境无法执行演示的原因并终止该演示。
func (t *demoT) Skipf(format string, args ...interface{}) {
	t.skipReason = fmt.Sprintf(format, args...)
	panic(demoSkip{})
}

// Cleanup 注册演示结束时执行的清理函数，执行顺序与注册顺序相反。
func (t *demoT) Cleanup(fn func()) {
	t.cleanups = append(t.cleanups, fn)
}

// demoFailure 标记由断言触发的预期中断。
type demoFailure struct{}

// demoSkip 标记由执行环境限制触发的预期跳过。
type demoSkip struct{}

// runDemo 执行演示函数并转换为 result。
func runDemo(name string, fn func(t *demoT)) result {
	t := &demoT{name: name}
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.recordRecovery(rec)
			}
			t.runCleanups()
		}()
		fn(t)
	}()
	if len(t.errors) != 0 {
		return result{name: name, err: fmt.Errorf("%s", strings.Join(t.errors, "; "))}
	}
	if t.skipReason != "" {
		return result{name: name, skipReason: t.skipReason}
	}
	return result{name: name}
}

// recordRecovery 将演示 panic 转换为可汇总的错误或跳过状态。
func (t *demoT) recordRecovery(rec interface{}) {
	switch rec.(type) {
	case demoFailure, demoSkip:
		return
	default:
		t.errors = append(t.errors, fmt.Sprintf("panic: %v", rec))
	}
}

// runCleanups 以 LIFO 顺序运行清理函数，并收集清理阶段的异常。
func (t *demoT) runCleanups() {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		func(cleanup func()) {
			defer func() {
				if rec := recover(); rec != nil {
					t.recordRecovery(rec)
				}
			}()
			cleanup()
		}(t.cleanups[i])
	}
}

// must 在 err 非空时让演示失败。
func must(t *demoT, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%v", err)
	}
}

// waitAddr 等待服务完成监听并返回实际地址。
func waitAddr(t *demoT, server *goserver.Server) net.Addr {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if addr := server.Addr(); addr != nil {
			return addr
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for server addr")
	return nil
}

// waitServerExit 等待 Start 退出。
func waitServerExit(t *demoT, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for server exit")
	}
}

// waitSessionCount 等待会话数量达到期望值。
func waitSessionCount(t *demoT, server *goserver.Server, want int) {
	t.Helper()
	waitCondition(t, 3*time.Second, fmt.Sprintf("session count = %d", want), func() bool {
		return server.CountSessions() == want
	})
	if server.CountSessions() != want {
		t.Fatalf("session count = %d, want %d", server.CountSessions(), want)
	}
}

// waitCondition 在超时前轮询条件，适合等待异步会话和回调状态。
func waitCondition(t *demoT, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", description)
}

// startServer 在后台启动服务，并注册兜底清理以确保演示失败时也能停止服务。
func startServer(t *demoT, server *goserver.Server) (net.Addr, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		server.Start()
		close(done)
	}()
	t.Cleanup(func() {
		if err := server.Stop(); err != nil {
			t.Errorf("cleanup server: %v", err)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("cleanup server: timeout waiting for Start to exit")
		}
	})
	addr := waitAddr(t, server)
	return addr, done
}

// hostPort 从 net.Addr 解析 host 与 port。
func hostPort(t *demoT, addr net.Addr) (string, int) {
	t.Helper()
	switch v := addr.(type) {
	case *net.TCPAddr:
		host := v.IP.String()
		if host == "::" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		return host, v.Port
	case *net.UDPAddr:
		host := v.IP.String()
		if host == "::" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		return host, v.Port
	default:
		host, port, err := net.SplitHostPort(addr.String())
		must(t, err)
		var p int
		_, err = fmt.Sscanf(port, "%d", &p)
		must(t, err)
		if host == "::" || host == "0.0.0.0" || host == "" {
			host = "127.0.0.1"
		}
		return host, p
	}
}

// beginEndFilter 返回示例统一使用的 Begin/End 过滤器。
func beginEndFilter() *filter.BeginEndMarkReceiveFilter {
	return &filter.BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	}
}

// wrapBeginEnd 将内容包装为 Begin/End 协议帧。
func wrapBeginEnd(f *filter.BeginEndMarkReceiveFilter, content []byte) []byte {
	out := make([]byte, 0, len(f.Begin)+len(content)+len(f.End))
	out = append(out, f.Begin...)
	out = append(out, content...)
	out = append(out, f.End...)
	return out
}

// beginEndActionToken 构造 BeginEnd 协议的 action token（不含 Begin/End 外壳）。
func beginEndActionToken(action string, body []byte) []byte {
	head := make([]byte, 4)
	binary.BigEndian.PutUint32(head, uint32(len(action)))
	out := make([]byte, 0, 4+len(action)+len(body))
	out = append(out, head...)
	out = append(out, action...)
	out = append(out, body...)
	return out
}

// fixedHeaderPacket 构造 FixedHeader 协议完整数据包。
func fixedHeaderPacket(action string, body []byte) []byte {
	actionLen := len(action)
	total := 8 + actionLen + len(body)
	packet := make([]byte, total)
	binary.BigEndian.PutUint32(packet[0:4], uint32(total))
	binary.BigEndian.PutUint32(packet[4:8], uint32(actionLen))
	copy(packet[8:8+actionLen], action)
	copy(packet[8+actionLen:], body)
	return packet
}

// generateTLSConfig 生成自签名证书，用于 TLS 演示。
func generateTLSConfig(t *demoT) (*tls.Config, *tls.Config) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "go-server-example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	must(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	must(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	must(t, err)

	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(certPEM) {
		t.Fatalf("append cert pem failed")
	}

	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	}
	clientTLS := &tls.Config{
		RootCAs:    rootPool,
		ServerName: "127.0.0.1",
		MinVersion: tls.VersionTLS12,
	}
	return serverTLS, clientTLS
}
