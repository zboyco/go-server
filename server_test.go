package goserver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zboyco/go-server/filter"
)

type valueModule struct{}

// Root 返回模块路由前缀。
func (valueModule) Root() string {
	return "/value"
}

// Say 是用于验证值类型模块注册的测试 action。
func (valueModule) Say(*AppSession, []byte) ([]byte, error) {
	return nil, nil
}

type pointerModule struct{}

// Root 返回模块路由前缀。
func (*pointerModule) Root() string {
	return "/pointer"
}

// TestSessionCloseWithEOFSendsBeforeClose 验证 EOF 会在 TCP 连接关闭前发出。
func TestSessionCloseWithEOFSendsBeforeClose(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	readResult := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := clientConn.Read(buf)
		readResult <- buf[:n]
	}()

	session := &AppSession{
		ID:      "tcp-session",
		network: TCP,
		conn:    serverConn,
		attr:    sessionAttr{values: make(map[string]interface{})},
	}
	session.closeWithEOF("test", []byte("EOF"))

	select {
	case got := <-readResult:
		if string(got) != "EOF" {
			t.Fatalf("EOF = %q, want %q", string(got), "EOF")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for EOF")
	}
	if !session.isClosed() || !session.IsClosed {
		t.Fatal("session should be marked closed")
	}
}

// TestSessionCloseIsIdempotent 验证重复关闭不会重复触发关闭回调。
func TestSessionCloseIsIdempotent(t *testing.T) {
	var closed int32
	session := &AppSession{
		ID:      "udp-session",
		network: UDP,
		attr:    sessionAttr{values: make(map[string]interface{})},
		closeTrigger: func(string) {
			atomic.AddInt32(&closed, 1)
		},
	}

	session.Close("first")
	session.Close("second")

	if got := atomic.LoadInt32(&closed); got != 1 {
		t.Fatalf("close trigger count = %d, want 1", got)
	}
}

// TestSessionAttrConcurrentAccess 验证会话属性可承受并发读写。
func TestSessionAttrConcurrentAccess(t *testing.T) {
	session := &AppSession{
		ID:   "attr-session",
		attr: sessionAttr{values: make(map[string]interface{})},
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "key"
			session.SetAttr(key, i)
			_, _ = session.GetAttr(key)
			_ = session.AddAttr(fmt.Sprintf("unique-%d", i), i)
			_ = session.DelAttr("missing")
		}(i)
	}
	wg.Wait()
}

// TestZeroValueSessionAttr 验证零值会话也可以安全设置属性。
func TestZeroValueSessionAttr(t *testing.T) {
	session := &AppSession{}
	session.SetAttr("key", "value")

	got, err := session.GetAttr("key")
	if err != nil {
		t.Fatalf("GetAttr returned error: %v", err)
	}
	if got != "value" {
		t.Fatalf("attr value = %v, want %v", got, "value")
	}
}

// TestSessionPoolCountDoesNotGoNegative 验证重复删除不会让会话计数变成负数。
func TestSessionPoolCountDoesNotGoNegative(t *testing.T) {
	pool := &sessionPool{list: make(chan *sessionHandle, 10)}
	stop := make(chan struct{})
	done := make(chan struct{})
	pool.start()
	go func() {
		pool.sessionPoolManager(stop)
		close(done)
	}()
	defer func() {
		pool.stop()
		close(stop)
		<-done
	}()

	session := &AppSession{ID: "session"}
	pool.addSession(session)
	waitForCount(t, pool, 1)

	pool.deleteSession(session)
	waitForCount(t, pool, 0)

	pool.deleteSession(session)
	waitForCount(t, pool, 0)
}

// TestSessionPoolStaleDeleteDoesNotRemoveReplacement 验证旧会话的删除事件不会误删同 ID 新会话。
func TestSessionPoolStaleDeleteDoesNotRemoveReplacement(t *testing.T) {
	pool := &sessionPool{list: make(chan *sessionHandle, 10)}
	stop := make(chan struct{})
	done := make(chan struct{})
	pool.start()
	go func() {
		pool.sessionPoolManager(stop)
		close(done)
	}()
	defer func() {
		pool.stop()
		close(stop)
		<-done
	}()

	oldSession := &AppSession{ID: "same-session"}
	newSession := &AppSession{ID: "same-session"}

	pool.addSession(oldSession)
	waitForCount(t, pool, 1)
	atomic.StoreUint32(&oldSession.closed, 1)

	pool.addSession(newSession)
	waitForSession(t, pool, newSession)

	pool.deleteSession(oldSession)
	waitForSession(t, pool, newSession)
	waitForCount(t, pool, 1)
}

// TestSessionPoolStoreSessionReplacesClosed 验证同步注册可替换已关闭会话且不重复计数。
func TestSessionPoolStoreSessionReplacesClosed(t *testing.T) {
	pool := &sessionPool{list: make(chan *sessionHandle, 10)}
	pool.start()
	defer pool.stop()

	oldSession := &AppSession{ID: "same-session"}
	session, created := pool.storeSession(oldSession)
	if !created || session != oldSession {
		t.Fatalf("store old session = (%p, %v), want (%p, true)", session, created, oldSession)
	}
	if got := pool.count(); got != 1 {
		t.Fatalf("session count = %d, want 1", got)
	}

	atomic.StoreUint32(&oldSession.closed, 1)
	newSession := &AppSession{ID: "same-session"}
	session, created = pool.storeSession(newSession)
	if !created || session != newSession {
		t.Fatalf("store new session = (%p, %v), want (%p, true)", session, created, newSession)
	}
	if got := pool.count(); got != 1 {
		t.Fatalf("session count = %d, want 1", got)
	}
	waitForSession(t, pool, newSession)
}

// TestRegisterModuleRejectsInvalidInput 验证模块注册对非法输入返回错误而不是 panic。
func TestRegisterModuleRejectsInvalidInput(t *testing.T) {
	server := newServer(TCP, "", 0, nil)

	var nilModule ActionModule
	if err := server.RegisterModule(nilModule); !errors.Is(err, ErrModuleFormat) {
		t.Fatalf("nil module error = %v, want %v", err, ErrModuleFormat)
	}

	var typedNil *pointerModule
	if err := server.RegisterModule(typedNil); !errors.Is(err, ErrModuleFormat) {
		t.Fatalf("typed nil module error = %v, want %v", err, ErrModuleFormat)
	}
}

// TestRegisterModuleSupportsValueModule 验证值类型模块可以被安全注册。
func TestRegisterModuleSupportsValueModule(t *testing.T) {
	server := newServer(TCP, "", 0, nil)
	if err := server.RegisterModule(valueModule{}); err != nil {
		t.Fatalf("RegisterModule returned error: %v", err)
	}
	if _, ok := server.actions["/value/say"]; !ok {
		t.Fatal("registered action /value/say not found")
	}
}

// TestActionPathNormalize 验证单 action 注册和调用使用一致的小写路径规则。
func TestActionPathNormalize(t *testing.T) {
	server := newServer(TCP, "", 0, nil)
	if err := server.Action("/Test", func(*AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("Action returned error: %v", err)
	}
	if err := server.hookAction("/test", &AppSession{}, nil); err != nil {
		t.Fatalf("hookAction returned error: %v", err)
	}
}

// TestSafeByteSliceBlocksUntilWriteAndCloses 验证 UDP 缓冲区为空时阻塞，关闭时唤醒。
func TestSafeByteSliceBlocksUntilWriteAndCloses(t *testing.T) {
	buffer := NewSafeByteSlice()
	readDone := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 8)
		n, _ := buffer.Read(buf)
		readDone <- buf[:n]
	}()

	select {
	case <-readDone:
		t.Fatal("Read returned before data was written")
	case <-time.After(20 * time.Millisecond):
	}

	if _, err := buffer.Write([]byte("udp")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	select {
	case got := <-readDone:
		if string(got) != "udp" {
			t.Fatalf("Read = %q, want %q", string(got), "udp")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for read")
	}

	if err := buffer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := buffer.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("Read after Close error = %v, want %v", err, io.EOF)
	}
}

// TestTCPServerOnMessageIntegration 验证 TCP 服务可以完成真实网络收发。
func TestTCPServerOnMessageIntegration(t *testing.T) {
	server := NewTCP("127.0.0.1", 0)
	server.IdleSessionTimeOut = 0
	if err := server.SetOnMessage(func(session *AppSession, token []byte) ([]byte, error) {
		return token, nil
	}); err != nil {
		t.Fatalf("SetOnMessage returned error: %v", err)
	}

	addr := startTestServer(t, server)
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatalf("DialTimeout returned error: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetDeadline returned error: %v", err)
	}

	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("ReadFull returned error: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("response = %q, want %q", got, "ping")
	}
}

// TestTCPServerStopSendsEOFIntegration 验证 Stop 会关闭会话并在关闭前发送 EOF。
func TestTCPServerStopSendsEOFIntegration(t *testing.T) {
	server := NewTCP("127.0.0.1", 0)
	server.IdleSessionTimeOut = 0
	if err := server.SetEOF([]byte("EOF")); err != nil {
		t.Fatalf("SetEOF returned error: %v", err)
	}
	if err := server.SetOnMessage(func(session *AppSession, token []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("SetOnMessage returned error: %v", err)
	}

	addr := startTestServer(t, server)
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatalf("DialTimeout returned error: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetDeadline returned error: %v", err)
	}
	waitForServerSessionCount(t, server, 1)

	if err := server.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if got := server.CountSessions(); got != 0 {
		t.Fatalf("session count after Stop = %d, want 0", got)
	}
	got := make([]byte, 3)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("ReadFull returned error: %v", err)
	}
	if string(got) != "EOF" {
		t.Fatalf("EOF = %q, want %q", got, "EOF")
	}
}

// TestUDPBeginEndRouteIntegration 验证 UDP 服务可以通过 BeginEnd 协议路由并响应。
func TestUDPBeginEndRouteIntegration(t *testing.T) {
	server := NewUDP("127.0.0.1", 0)
	server.IdleSessionTimeOut = 0
	receiveFilter := &filter.BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	}
	if err := server.SetReceiveFilter(receiveFilter); err != nil {
		t.Fatalf("SetReceiveFilter returned error: %v", err)
	}
	if err := server.Action("/say", func(session *AppSession, token []byte) ([]byte, error) {
		return token, nil
	}); err != nil {
		t.Fatalf("Action returned error: %v", err)
	}

	addr := startTestServer(t, server)
	udpAddr, err := net.ResolveUDPAddr("udp", addr.String())
	if err != nil {
		t.Fatalf("ResolveUDPAddr returned error: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		t.Fatalf("DialUDP returned error: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetDeadline returned error: %v", err)
	}

	packet := beginEndActionPacket(receiveFilter.Begin, receiveFilter.End, "/say", []byte("hello"))
	if _, err := conn.Write(packet); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("ReadFull returned error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("response = %q, want %q", got, "hello")
	}
}

// TestUDPHandleClientReusesSynchronouslyStoredSession 验证连续 UDP 包会复用同步注册的 session。
func TestUDPHandleClientReusesSynchronouslyStoredSession(t *testing.T) {
	server := NewUDP("127.0.0.1", 0)
	server.IdleSessionTimeOut = 0
	server.sessionSource.start()
	defer func() {
		server.closeAllSessions("test done")
		server.sessionSource.stop()
		server.sessionSource.clear()
	}()
	if err := server.SetOnMessage(func(session *AppSession, token []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("SetOnMessage returned error: %v", err)
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP returned error: %v", err)
	}
	defer conn.Close()

	var newSessions int32
	server.onNewSessionRegister = func(*AppSession) {
		atomic.AddInt32(&newSessions, 1)
	}
	clientAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001}

	server.handleUDPClient(conn, clientAddr, []byte("first\n"))
	server.handleUDPClient(conn, clientAddr, []byte("second\n"))

	if got := atomic.LoadInt32(&newSessions); got != 1 {
		t.Fatalf("new session count = %d, want 1", got)
	}
	if got := server.CountSessions(); got != 1 {
		t.Fatalf("session count = %d, want 1", got)
	}
}

// TestServerShutdownContextIntegration 验证 Shutdown 可以在 context 期限内停止服务。
func TestServerShutdownContextIntegration(t *testing.T) {
	server := NewTCP("127.0.0.1", 0)
	server.IdleSessionTimeOut = 0
	if err := server.SetOnMessage(func(session *AppSession, token []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("SetOnMessage returned error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		server.Start()
		close(done)
	}()
	waitForServerAddr(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	waitForServerExit(t, done)
}

func waitForCount(t *testing.T, pool *sessionPool, want int) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := pool.count(); got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session count = %d, want %d", pool.count(), want)
}

func waitForSession(t *testing.T, pool *sessionPool, want *AppSession) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, err := pool.getSessionByID(want.ID)
		if err == nil && got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	got, _ := pool.getSessionByID(want.ID)
	t.Fatalf("session = %p, want %p", got, want)
}

func startTestServer(t *testing.T, server *Server) net.Addr {
	t.Helper()

	done := make(chan struct{})
	go func() {
		server.Start()
		close(done)
	}()
	t.Cleanup(func() {
		if err := server.Stop(); err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
		waitForServerExit(t, done)
	})

	return waitForServerAddr(t, server)
}

func waitForServerAddr(t *testing.T, server *Server) net.Addr {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if addr := server.Addr(); addr != nil {
			return addr
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timeout waiting for server address")
	return nil
}

func waitForServerExit(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server to stop")
	}
}

func waitForServerSessionCount(t *testing.T, server *Server, want int) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := server.CountSessions(); got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session count = %d, want %d", server.CountSessions(), want)
}

func beginEndActionPacket(begin, end []byte, action string, body []byte) []byte {
	head := make([]byte, 4)
	binary.BigEndian.PutUint32(head, uint32(len(action)))

	packet := make([]byte, 0, len(begin)+len(head)+len(action)+len(body)+len(end))
	packet = append(packet, begin...)
	packet = append(packet, head...)
	packet = append(packet, action...)
	packet = append(packet, body...)
	packet = append(packet, end...)
	return packet
}
