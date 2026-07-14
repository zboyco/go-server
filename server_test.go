package goserver

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zboyco/go-server/v2/filter"
)

type valueModule struct{}

// Root returns the module route prefix.
func (valueModule) Root() string { return "/value" }

// Say is a reflected test action.
func (valueModule) Say(context.Context, *AppSession, []byte) ([]byte, error) {
	return nil, nil
}

type pointerModule struct{}

// Root returns the pointer module route prefix.
func (*pointerModule) Root() string { return "/pointer" }

type caseConflictModule struct{}

// Root returns the case-conflict test route prefix.
func (*caseConflictModule) Root() string { return "/conflict" }

// FOO is intentionally case-equivalent to Foo after route normalization.
func (*caseConflictModule) FOO(context.Context, *AppSession, []byte) ([]byte, error) {
	return nil, nil
}

// Foo is intentionally case-equivalent to FOO after route normalization.
func (*caseConflictModule) Foo(context.Context, *AppSession, []byte) ([]byte, error) {
	return nil, nil
}

type nilMiddlewareModule struct{}

// Root returns the nil-middleware test route prefix.
func (*nilMiddlewareModule) Root() string { return "/nil-middleware" }

// Run is a reflected test action.
func (*nilMiddlewareModule) Run(context.Context, *AppSession, []byte) ([]byte, error) {
	return nil, nil
}

// MiddlewaresBeforeAction returns an invalid module middleware list.
func (*nilMiddlewareModule) MiddlewaresBeforeAction() Middlewares {
	return Middlewares{nil}
}

// blockingSlogHandler 用于验证自定义日志处理器不会阻塞监听循环退出。
type blockingSlogHandler struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

// Enabled 允许测试 logger 接收所有级别记录。
func (*blockingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

// Handle 在第一条记录到达后阻塞，直到测试显式释放。
func (handler *blockingSlogHandler) Handle(context.Context, slog.Record) error {
	handler.once.Do(func() { close(handler.started) })
	<-handler.release
	return nil
}

// WithAttrs 返回共享阻塞状态的 handler。
func (handler *blockingSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }

// WithGroup 返回共享阻塞状态的 handler。
func (handler *blockingSlogHandler) WithGroup(string) slog.Handler { return handler }

// TestNewRejectsInvalidConfig verifies constructor validation.
func TestNewRejectsInvalidConfig(t *testing.T) {
	if _, err := New(Network("invalid"), "127.0.0.1:0"); !errors.Is(err, ErrUnsupportedNetwork) {
		t.Fatalf("invalid network error = %v", err)
	}
	if _, err := NewTCP(""); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty address error = %v", err)
	}
	if _, err := NewUDP("127.0.0.1:0", WithMaxDatagramSize(0)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid datagram size error = %v", err)
	}
}

// TestOptionsAndRegistration exercises the complete v1 configuration surface.
func TestOptionsAndRegistration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := NewTCP("127.0.0.1:0",
		WithIdleTimeout(time.Second),
		WithWriteTimeout(2*time.Second),
		WithShutdownWriteTimeout(3*time.Second),
		WithMaxFrameSize(1024),
		WithMaxConnections(2),
		WithTLS(&tls.Config{}),
		WithTLSHandshakeTimeout(4*time.Second),
		WithLogger(logger),
		WithUDPWorkers(2),
		WithUDPQueueSize(3),
		WithMaxDatagramSize(4096),
		WithUDPDispatchMode(UDPDispatchInline),
	)
	if err != nil {
		t.Fatal(err)
	}
	if server.config.idleTimeout != time.Second || server.config.writeTimeout != 2*time.Second || server.config.maxFrameSize != 1024 || server.config.tlsHandshakeTimeout != 4*time.Second || server.config.udpDispatchMode != UDPDispatchInline {
		t.Fatalf("config = %+v", server.config)
	}
	if err := server.SetOnError(func(error) {}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnSessionClosed(func(*AppSession, string) {}); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterConnectionFilterTCP(func(net.Conn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterConnectionFilterUDP(func(*net.UDPAddr) error { return nil }); err != nil {
		t.Fatal(err)
	}
	identity := func(_ context.Context, _ *AppSession, data []byte) ([]byte, error) { return data, nil }
	if err := server.RegisterSendPacketFilter(Middlewares{identity}); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterBeforeMiddlewares(Middlewares{identity}); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterAfterMiddlewares(Middlewares{identity}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.GetSessionByID("missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing session error = %v, want %v", err, ErrSessionNotFound)
	}
	if err := server.SetSplitFunc(nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil split error = %v", err)
	}
	var typedNil *filter.FixedHeaderReceiveFilter
	if err := server.SetReceiveFilter(typedNil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("typed nil filter error = %v", err)
	}
	if err := server.Action("invalid", identity); !errors.Is(err, ErrPathFormat) {
		t.Fatalf("invalid path error = %v", err)
	}
	if err := server.Action("/empty"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty action error = %v", err)
	}
	if err := server.Action("/nil", nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil action error = %v", err)
	}
}

// TestOptionValidationBranches verifies invalid cross-field combinations.
func TestOptionValidationBranches(t *testing.T) {
	tests := []struct {
		name    string
		network Network
		option  Option
	}{
		{name: "negative idle", network: TCP, option: WithIdleTimeout(-1)},
		{name: "negative write", network: TCP, option: WithWriteTimeout(-1)},
		{name: "negative shutdown", network: TCP, option: WithShutdownWriteTimeout(-1)},
		{name: "zero frame", network: TCP, option: WithMaxFrameSize(0)},
		{name: "negative connections", network: TCP, option: WithMaxConnections(-1)},
		{name: "nil TLS", network: TCP, option: WithTLS(nil)},
		{name: "negative TLS handshake", network: TCP, option: WithTLSHandshakeTimeout(-1)},
		{name: "nil logger", network: TCP, option: WithLogger(nil)},
		{name: "zero workers", network: UDP, option: WithUDPWorkers(0)},
		{name: "zero queue", network: UDP, option: WithUDPQueueSize(0)},
		{name: "large datagram", network: UDP, option: WithMaxDatagramSize(64*1024 + 1)},
		{name: "dispatch mode", network: UDP, option: WithUDPDispatchMode(UDPDispatchMode(255))},
		{name: "UDP TLS", network: UDP, option: WithTLS(&tls.Config{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.network, "127.0.0.1:0", test.option); err == nil {
				t.Fatal("invalid option should fail")
			}
		})
	}
	if _, err := NewTCP("127.0.0.1:0", nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil option error = %v", err)
	}
}

// TestServerCloseBeforeServe verifies a never-started server can be closed safely.
func TestServerCloseBeforeServe(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0")
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := server.Serve(); !errors.Is(err, ErrServerClosed) {
		t.Fatalf("Serve after Close error = %v", err)
	}
}

// TestFinishCleanupIsConcurrentIdempotent verifies both lifecycle completion signals close exactly once.
func TestFinishCleanupIsConcurrentIdempotent(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0")
	var waitGroup sync.WaitGroup
	for index := 0; index < 64; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			server.finishCleanup()
		}()
	}
	waitGroup.Wait()
	select {
	case <-server.serveDone:
	default:
		t.Fatal("serveDone was not closed")
	}
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained: %v", err)
	}
	server.lifecycleMu.Lock()
	state := server.state
	server.lifecycleMu.Unlock()
	if state != serverStopped {
		t.Fatalf("server state = %v, want stopped", state)
	}
}

// TestSessionCloseWithEOFSendsBeforeClose verifies the bounded EOF path.
func TestSessionCloseWithEOFSendsBeforeClose(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0")
	server.ctx, server.cancel = context.WithCancel(context.Background())
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	session := newTCPSession(server, "session", serverConn)

	result := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 3)
		_, _ = io.ReadFull(clientConn, buffer)
		result <- buffer
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.closeWithEOF(ctx, "test", []byte("EOF")); err != nil {
		t.Fatalf("closeWithEOF: %v", err)
	}
	if got := <-result; string(got) != "EOF" {
		t.Fatalf("EOF = %q", got)
	}
	if !session.Closed() {
		t.Fatal("session should be closed")
	}
}

// TestSessionCloseIsIdempotent verifies a close callback runs once.
func TestSessionCloseIsIdempotent(t *testing.T) {
	var count atomic.Int32
	session := &AppSession{ID: "session", ctx: context.Background(), cancel: func() {}}
	session.closeTrigger = func(string) { count.Add(1) }
	if err := session.Close("first"); err != nil {
		t.Fatal(err)
	}
	if err := session.Close("second"); err != nil {
		t.Fatal(err)
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("close count = %d", got)
	}
}

// TestSessionInterruptMarksInactiveBeforeFinalize verifies transport interruption immediately updates public state.
func TestSessionInterruptMarksInactiveBeforeFinalize(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0")
	server.ctx, server.cancel = context.WithCancel(context.Background())
	var callbacks atomic.Int32
	server.onSessionClosed = func(*AppSession, string) {
		callbacks.Add(1)
	}
	session := newTCPSession(server, "interrupted", nil)
	server.bindSession(session)
	if stored, created := server.sessions.store(session); !created || stored != session {
		t.Fatalf("store = (%p, %v)", stored, created)
	}

	session.interrupt()
	if !session.Closed() || !errors.Is(session.Context().Err(), context.Canceled) {
		t.Fatalf("interrupted state: closed=%v context=%v", session.Closed(), session.Context().Err())
	}
	if server.CountSessions() != 0 || len(server.Sessions()) != 0 || len(server.FindSessions(func(map[string]any) bool { return true })) != 0 {
		t.Fatalf("interrupted session remained public: count=%d sessions=%d", server.CountSessions(), len(server.Sessions()))
	}
	if _, err := server.GetSessionByID(session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetSessionByID error = %v, want %v", err, ErrSessionNotFound)
	}
	if err := session.Send(context.Background(), []byte("x")); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("Send error = %v, want %v", err, ErrSessionClosed)
	}
	if callbacks.Load() != 0 || server.Stats().ClosedSessions != 0 || len(server.allSessions()) != 1 {
		t.Fatalf("interrupt finalized session: callbacks=%d stats=%d internal=%d", callbacks.Load(), server.Stats().ClosedSessions, len(server.allSessions()))
	}

	if !session.beginClose() {
		t.Fatal("interrupt unexpectedly claimed the final close flow")
	}
	session.finalizeClose("test finalize")
	if callbacks.Load() != 1 || server.Stats().ClosedSessions != 1 || len(server.allSessions()) != 0 {
		t.Fatalf("final state: callbacks=%d stats=%d internal=%d", callbacks.Load(), server.Stats().ClosedSessions, len(server.allSessions()))
	}
}

// TestConcurrentSessionCloseTransitionsOnce verifies concurrent close paths update counters and callbacks once.
func TestConcurrentSessionCloseTransitionsOnce(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0")
	server.ctx, server.cancel = context.WithCancel(context.Background())
	var callbacks atomic.Int32
	server.onSessionClosed = func(*AppSession, string) {
		callbacks.Add(1)
	}
	session := newTCPSession(server, "concurrent-close", nil)
	server.bindSession(session)
	if stored, created := server.sessions.store(session); !created || stored != session {
		t.Fatalf("store = (%p, %v)", stored, created)
	}

	var waitGroup sync.WaitGroup
	for index := 0; index < 96; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			switch index % 3 {
			case 0:
				_ = session.Close("concurrent close")
			case 1:
				session.interrupt()
			default:
				session.forceClose("concurrent force close")
			}
		}(index)
	}
	waitGroup.Wait()
	if !session.Closed() || server.CountSessions() != 0 || len(server.allSessions()) != 0 {
		t.Fatalf("final session state: closed=%v active=%d internal=%d", session.Closed(), server.CountSessions(), len(server.allSessions()))
	}
	if callbacks.Load() != 1 || server.Stats().ClosedSessions != 1 {
		t.Fatalf("close accounting: callbacks=%d stats=%d", callbacks.Load(), server.Stats().ClosedSessions)
	}
}

// TestSessionPublicIOAndAccessors verifies filtered/raw sends and transport accessors.
func TestSessionPublicIOAndAccessors(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithWriteTimeout(0))
	server.ctx, server.cancel = context.WithCancel(context.Background())
	server.sendPacketFilter = Middlewares{
		func(_ context.Context, _ *AppSession, data []byte) ([]byte, error) {
			return append(data, '!'), nil
		},
	}
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	session := newTCPSession(server, "io", serverConn)
	if session.Context() == nil || session.Network() != TCP || session.RemoteAddr() == nil {
		t.Fatal("session accessors returned invalid values")
	}
	read := make(chan string, 2)
	go func() {
		buffer := make([]byte, 2)
		_, _ = io.ReadFull(clientConn, buffer)
		read <- string(buffer)
		buffer = make([]byte, 1)
		_, _ = io.ReadFull(clientConn, buffer)
		read <- string(buffer)
	}()
	if err := session.Send(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := session.SendRaw(context.Background(), []byte("r")); err != nil {
		t.Fatal(err)
	}
	if got := <-read; got != "x!" {
		t.Fatalf("filtered send = %q", got)
	}
	if got := <-read; got != "r" {
		t.Fatalf("raw send = %q", got)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.Send(canceled, []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Send error = %v", err)
	}
	session.SetAttr("key", "value")
	if err := session.DelAttr("key"); err != nil {
		t.Fatal(err)
	}
	if err := session.DelAttr("key"); err == nil {
		t.Fatal("deleting a missing attribute should fail")
	}
	_ = session.Close("done")
}

// TestUDPSessionSendRaw verifies the datagram write branch and address accessors.
func TestUDPSessionSendRaw(t *testing.T) {
	server := mustNewUDPServer(t, "127.0.0.1:0")
	server.ctx, server.cancel = context.WithCancel(context.Background())
	serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()
	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	session := newUDPSession(server, "udp", serverConn, clientConn.LocalAddr().(*net.UDPAddr).AddrPort())
	if session.Network() != UDP || session.RemoteAddr() == nil {
		t.Fatal("UDP accessors returned invalid values")
	}
	if err := session.SendRaw(context.Background(), []byte("udp")); err != nil {
		t.Fatal(err)
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 3)
	if _, _, err := clientConn.ReadFromUDP(buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "udp" {
		t.Fatalf("UDP response = %q", buffer)
	}
	_ = session.Close("done")
}

// TestSessionAttrConcurrentAccess verifies concurrent attribute access.
func TestSessionAttrConcurrentAccess(t *testing.T) {
	session := &AppSession{}
	var waitGroup sync.WaitGroup
	for index := 0; index < 100; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			session.SetAttr("shared", index)
			_, _ = session.GetAttr("shared")
			_ = session.AddAttr(fmt.Sprintf("key-%d", index), index)
		}(index)
	}
	waitGroup.Wait()
}

// TestSessionStoreReplaceAndSnapshots verifies synchronous replacement and selection.
func TestSessionStoreReplaceAndSnapshots(t *testing.T) {
	var store sessionStore
	oldSession := &AppSession{ID: "same"}
	if stored, created := store.store(oldSession); !created || stored != oldSession {
		t.Fatalf("initial store = (%p, %v)", stored, created)
	}
	if !oldSession.beginClose() {
		t.Fatal("old session did not start closing")
	}
	newSession := &AppSession{ID: "same"}
	newSession.SetAttr("role", "admin")
	if stored, created := store.store(newSession); !created || stored != newSession {
		t.Fatalf("replacement store = (%p, %v)", stored, created)
	}
	if store.len() != 1 || len(store.sessions()) != 1 {
		t.Fatalf("store sizes = %d/%d", store.len(), len(store.sessions()))
	}
	found := store.find(func(attrs map[string]any) bool { return attrs["role"] == "admin" })
	if len(found) != 1 || found[0] != newSession {
		t.Fatalf("found sessions = %#v", found)
	}
	if !newSession.beginClose() {
		t.Fatal("new session did not start closing")
	}
	if !store.delete(newSession) || store.len() != 0 {
		t.Fatal("replacement was not deleted")
	}
}

// TestSessionStoreTreatsClosingSessionAsInactive verifies closing endpoints can be replaced immediately.
func TestSessionStoreTreatsClosingSessionAsInactive(t *testing.T) {
	server := mustNewUDPServer(t, "127.0.0.1:0")
	oldSession := &AppSession{ID: "same", server: server}
	if stored, created := server.sessions.store(oldSession); !created || stored != oldSession {
		t.Fatalf("initial store = (%p, %v)", stored, created)
	}
	if !oldSession.beginClose() {
		t.Fatal("old session did not enter closing")
	}
	if !oldSession.Closed() {
		t.Fatal("Closed returned false for closing session")
	}
	if server.CountSessions() != 0 || len(server.Sessions()) != 0 {
		t.Fatalf("closing session remained active: count=%d sessions=%d", server.CountSessions(), len(server.Sessions()))
	}
	if _, err := server.GetSessionByID(oldSession.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("closing GetSessionByID error = %v, want %v", err, ErrSessionNotFound)
	}

	newSession := &AppSession{ID: "same", server: server}
	if stored, created := server.sessions.store(newSession); !created || stored != newSession {
		t.Fatalf("replacement store = (%p, %v)", stored, created)
	}
	if server.CountSessions() != 1 || len(server.Sessions()) != 1 {
		t.Fatalf("replacement session missing: count=%d sessions=%d", server.CountSessions(), len(server.Sessions()))
	}
}

// TestSessionStoreActivationCompensatesClosingCandidate verifies publication cannot resurrect an inactive session count.
func TestSessionStoreActivationCompensatesClosingCandidate(t *testing.T) {
	var store sessionStore
	session := &AppSession{ID: "closing-before-store"}
	if !session.beginClose() {
		t.Fatal("session did not start closing")
	}
	if stored, created := store.store(session); !created || stored != session {
		t.Fatalf("store = (%p, %v)", stored, created)
	}
	if store.len() != 0 || len(store.sessions()) != 0 || len(store.all()) != 1 {
		t.Fatalf("closing store state: active=%d public=%d internal=%d", store.len(), len(store.sessions()), len(store.all()))
	}
	if !store.delete(session) || store.residentCount.Load() != 0 {
		t.Fatalf("closing session was not deleted: resident=%d", store.residentCount.Load())
	}
}

// TestSessionStoreRepeatedInstanceKeepsInactiveTrigger verifies a repeated store cannot leak the active count.
func TestSessionStoreRepeatedInstanceKeepsInactiveTrigger(t *testing.T) {
	var store sessionStore
	session := &AppSession{ID: "repeated-instance"}
	if stored, created := store.store(session); !created || stored != session {
		t.Fatalf("initial store = (%p, %v)", stored, created)
	}
	if stored, created := store.store(session); created || stored != session {
		t.Fatalf("repeated store = (%p, %v)", stored, created)
	}
	if !session.beginClose() {
		t.Fatal("session did not start closing")
	}
	if store.len() != 0 {
		t.Fatalf("active sessions after repeated store close = %d", store.len())
	}
	if !store.delete(session) || store.residentCount.Load() != 0 {
		t.Fatalf("repeated session was not deleted: resident=%d", store.residentCount.Load())
	}
}

// TestClosedSessionsCountsReplacedSession verifies replacement cannot hide a close event.
func TestClosedSessionsCountsReplacedSession(t *testing.T) {
	server := mustNewUDPServer(t, "127.0.0.1:0")
	oldSession := &AppSession{ID: "same"}
	server.bindSession(oldSession)
	if stored, created := server.sessions.store(oldSession); !created || stored != oldSession {
		t.Fatalf("initial store = (%p, %v)", stored, created)
	}

	// Reproduce the window after the old session leaves the public active set but
	// before its final close trigger attempts to remove the stored instance.
	if !oldSession.beginClose() {
		t.Fatal("old session did not start closing")
	}
	replacement := &AppSession{ID: "same"}
	server.bindSession(replacement)
	if stored, created := server.sessions.store(replacement); !created || stored != replacement {
		t.Fatalf("replacement store = (%p, %v)", stored, created)
	}
	oldSession.finalizeClose("replaced")
	replacement.forceClose("done")

	if got := server.Stats().ClosedSessions; got != 2 {
		t.Fatalf("closed sessions = %d, want 2", got)
	}
	if server.CountSessions() != 0 {
		t.Fatalf("active sessions = %d", server.CountSessions())
	}
	if len(server.allSessions()) != 0 {
		t.Fatalf("internal sessions after drain = %d", len(server.allSessions()))
	}
}

// TestRegisterModuleAndActionPath verifies v1 context-aware route registration.
func TestRegisterModuleAndActionPath(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0")
	var typedNil *pointerModule
	if err := server.RegisterModule(typedNil); !errors.Is(err, ErrModuleFormat) {
		t.Fatalf("typed nil error = %v", err)
	}
	if err := server.RegisterModule(valueModule{}); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	conflictServer := mustNewTCPServer(t, "127.0.0.1:0")
	if err := conflictServer.RegisterModule(&caseConflictModule{}); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("case-equivalent module routes error = %v", err)
	}
	if _, ok := server.actions["/value/say"]; !ok {
		t.Fatal("reflected action was not registered")
	}
	if err := server.Action("/Echo", func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.hookAction(context.Background(), "/echo", &AppSession{}, nil); err != nil {
		t.Fatalf("hookAction: %v", err)
	}

	original := func(context.Context, *AppSession, []byte) ([]byte, error) {
		return []byte("original"), nil
	}
	actions := []ActionFunc{original}
	if err := server.Action("/isolated", actions...); err != nil {
		t.Fatal(err)
	}
	actions[0] = func(context.Context, *AppSession, []byte) ([]byte, error) {
		return []byte("mutated"), nil
	}
	response, err := server.actions["/isolated"][0](context.Background(), nil, nil)
	if err != nil || string(response) != "original" {
		t.Fatalf("isolated action response = %q, %v", response, err)
	}
}

// TestMiddlewareRegistrationRejectsNil verifies invalid pipelines fail before Serve.
func TestMiddlewareRegistrationRejectsNil(t *testing.T) {
	tests := []struct {
		name     string
		register func(*Server) error
	}{
		{name: "send filter", register: func(server *Server) error {
			return server.RegisterSendPacketFilter(Middlewares{nil})
		}},
		{name: "before", register: func(server *Server) error {
			return server.RegisterBeforeMiddlewares(Middlewares{nil})
		}},
		{name: "after", register: func(server *Server) error {
			return server.RegisterAfterMiddlewares(Middlewares{nil})
		}},
		{name: "module", register: func(server *Server) error {
			return server.RegisterModule(&nilMiddlewareModule{})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := mustNewTCPServer(t, "127.0.0.1:0")
			if err := test.register(server); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("registration error = %v", err)
			}
		})
	}
}

// TestActionPipelineAndErrorBranches verifies ordered middleware and short-circuit errors.
func TestActionPipelineAndErrorBranches(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithWriteTimeout(0))
	server.ctx, server.cancel = context.WithCancel(context.Background())
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	session := newTCPSession(server, "pipeline", serverConn)
	server.middlewaresBefore = Middlewares{
		func(_ context.Context, _ *AppSession, data []byte) ([]byte, error) {
			return append([]byte("before:"), data...), nil
		},
	}
	server.middlewaresAfter = Middlewares{
		func(_ context.Context, _ *AppSession, data []byte) ([]byte, error) {
			return append(data, []byte(":after")...), nil
		},
	}
	server.actions["/pipeline"] = []ActionFunc{
		func(_ context.Context, _ *AppSession, data []byte) ([]byte, error) {
			return append(data, []byte(":action")...), nil
		},
	}
	want := "before:value:action:after"
	read := make(chan string, 1)
	go func() {
		buffer := make([]byte, len(want))
		_, _ = io.ReadFull(clientConn, buffer)
		read <- string(buffer)
	}()
	if err := server.hookAction(context.Background(), "/PIPELINE", session, []byte("value")); err != nil {
		t.Fatal(err)
	}
	if got := <-read; got != want {
		t.Fatalf("pipeline response = %q", got)
	}
	if err := server.hookAction(context.Background(), "/missing", session, nil); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("missing action error = %v", err)
	}
	sentinel := errors.New("stop")
	server.middlewaresBefore = Middlewares{func(context.Context, *AppSession, []byte) ([]byte, error) { return nil, sentinel }}
	if err := server.hookAction(context.Background(), "/pipeline", session, nil); !errors.Is(err, sentinel) {
		t.Fatalf("before error = %v", err)
	}
	server.middlewaresBefore = nil
	server.actions["/pipeline"] = []ActionFunc{func(context.Context, *AppSession, []byte) ([]byte, error) { return nil, sentinel }}
	if err := server.hookAction(context.Background(), "/pipeline", session, nil); !errors.Is(err, sentinel) {
		t.Fatalf("action error = %v", err)
	}
	server.actions["/pipeline"] = []ActionFunc{func(context.Context, *AppSession, []byte) ([]byte, error) { return nil, nil }}
	server.middlewaresAfter = Middlewares{func(context.Context, *AppSession, []byte) ([]byte, error) { return nil, sentinel }}
	if err := server.hookAction(context.Background(), "/pipeline", session, nil); !errors.Is(err, sentinel) {
		t.Fatalf("after error = %v", err)
	}
	wrapped := formatSessionError(session, "test", sentinel)
	if !errors.Is(wrapped, sentinel) {
		t.Fatalf("wrapped error = %v", wrapped)
	}
	_ = session.Close("done")
}

// TestInternalDefensiveBranches verifies invalid store values and pool buffer rejection.
func TestInternalDefensiveBranches(t *testing.T) {
	var store sessionStore
	store.values.Store("bad", 1)
	if session, created := store.store(&AppSession{ID: "bad"}); session != nil || created {
		t.Fatalf("invalid store result = (%v, %v)", session, created)
	}
	store.values.Store("session", &AppSession{ID: "session"})
	storedSession := &AppSession{ID: "stored"}
	storedSession.activeCounted.Store(true)
	store.values.Store(storedSession.ID, storedSession)
	store.residentCount.Store(1)
	store.openCount.Store(1)
	store.clear()
	if store.len() != 0 || storedSession.activeCounted.Load() {
		t.Fatalf("store clear state: count=%d active=%v", store.len(), storedSession.activeCounted.Load())
	}
	pool := newDatagramBufferPool()
	pool.put(make([]byte, 1))
	if isNilInterface(1) {
		t.Fatal("non-nil scalar reported nil")
	}
	session := &AppSession{network: TCP}
	if session.RemoteAddr() != nil {
		t.Fatal("nil TCP connection should have nil remote address")
	}
}

// TestTCPServerOnMessageIntegration verifies a real TCP request/response.
func TestTCPServerOnMessageIntegration(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	if err := server.SetOnMessage(func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
		return token, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "ping" {
		t.Fatalf("response = %q", response)
	}
	waitForSessionCount(t, server, 1)
	deadline := time.Now().Add(time.Second)
	stats := server.Stats()
	for (stats.BytesRead == 0 || stats.BytesWritten == 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		stats = server.Stats()
	}
	if stats.BytesRead == 0 || stats.BytesWritten == 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.AcceptedConnections != 1 {
		t.Fatalf("accepted connections = %d, want 1", stats.AcceptedConnections)
	}
}

// TestTCPExplicitSessionCloseDoesNotReportError verifies expected close errors stay silent.
func TestTCPExplicitSessionCloseDoesNotReportError(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	errorsSeen := make(chan error, 1)
	if err := server.SetOnError(func(err error) {
		select {
		case errorsSeen <- err:
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForSessionCount(t, server, 1)
	if err := server.Sessions()[0].Close("test close"); err != nil {
		t.Fatal(err)
	}
	waitForSessionCount(t, server, 0)

	select {
	case err := <-errorsSeen:
		t.Fatalf("explicit session close reported error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestStopRequestsFromActionsDoNotDeadlock verifies callback-safe stop entry points.
func TestStopRequestsFromActionsDoNotDeadlock(t *testing.T) {
	tests := []struct {
		name     string
		network  Network
		mode     UDPDispatchMode
		graceful bool
	}{
		{name: "TCP close", network: TCP},
		{name: "UDP queued shutdown", network: UDP, mode: UDPDispatchQueued, graceful: true},
		{name: "UDP inline close", network: UDP, mode: UDPDispatchInline},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := []Option{WithIdleTimeout(0)}
			if test.network == UDP {
				options = append(options, WithUDPDispatchMode(test.mode))
			}
			server, err := New(test.network, "127.0.0.1:0", options...)
			if err != nil {
				t.Fatal(err)
			}
			var actionCalls atomic.Int32
			if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
				actionCalls.Add(1)
				if test.graceful {
					server.RequestShutdown()
				} else {
					server.RequestClose()
				}
				return nil, nil
			}); err != nil {
				t.Fatal(err)
			}
			address, done := startUnmanagedTestServer(t, server)
			if test.network == TCP {
				conn, err := net.DialTimeout("tcp", address.String(), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err := conn.Write([]byte("stop\n")); err != nil {
					t.Fatal(err)
				}
			} else {
				conn, err := net.DialUDP("udp", nil, address.(*net.UDPAddr))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err := conn.Write([]byte("stop\n")); err != nil {
					t.Fatal(err)
				}
			}
			waitUnmanagedTestServer(t, done)
			if actionCalls.Load() != 1 {
				t.Fatalf("action calls = %d", actionCalls.Load())
			}
		})
	}
}

// TestRequestCloseFromOnErrorDoesNotDeadlock verifies read-loop callbacks can stop inline UDP.
func TestRequestCloseFromOnErrorDoesNotDeadlock(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mustNewUDPServer(t, "127.0.0.1:0",
		WithIdleTimeout(0),
		WithMaxDatagramSize(1),
		WithUDPDispatchMode(UDPDispatchInline),
		WithLogger(logger),
	)
	var callbacks atomic.Int32
	if err := server.SetOnError(func(err error) {
		if errors.Is(err, ErrDatagramTooLarge) {
			callbacks.Add(1)
			server.RequestClose()
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	address, done := startUnmanagedTestServer(t, server)
	conn, err := net.DialUDP("udp", nil, address.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("xx")); err != nil {
		t.Fatal(err)
	}
	waitUnmanagedTestServer(t, done)
	if callbacks.Load() != 1 {
		t.Fatalf("error callbacks = %d", callbacks.Load())
	}
}

// TestBlockedStartupLoggerCannotRetainServe verifies startup logging participates only in drain.
func TestBlockedStartupLoggerCannotRetainServe(t *testing.T) {
	loggerStarted := make(chan struct{})
	releaseLogger := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseLogger) }) }
	defer unblock()
	logger := slog.New(&blockingSlogHandler{started: loggerStarted, release: releaseLogger})
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithLogger(logger))
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	_, serveDone := startUnmanagedTestServer(t, server)
	select {
	case <-loggerStarted:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for startup logger")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v", err)
	}
	waitUnmanagedTestServer(t, serveDone)
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer drainCancel()
	if err := server.WaitDrained(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDrained while logger is blocked = %v", err)
	}

	unblock()
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained after logger release: %v", err)
	}
}

// TestUDPBlockedOnErrorCannotRetainServe verifies read-loop errors execute in a drained worker.
func TestUDPBlockedOnErrorCannotRetainServe(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mustNewUDPServer(t, "127.0.0.1:0",
		WithIdleTimeout(0),
		WithMaxDatagramSize(1),
		WithUDPDispatchMode(UDPDispatchInline),
		WithLogger(logger),
	)
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	defer unblock()
	if err := server.SetOnError(func(err error) {
		if errors.Is(err, ErrDatagramTooLarge) {
			startOnce.Do(func() { close(callbackStarted) })
			<-releaseCallback
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	address, serveDone := startUnmanagedTestServer(t, server)
	conn, err := net.DialUDP("udp", nil, address.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("xx")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for OnError")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown(ctx) }()
	select {
	case err := <-shutdownDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		unblock()
		<-shutdownDone
		t.Fatal("Shutdown was retained by blocked OnError")
	}
	waitUnmanagedTestServer(t, serveDone)
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer drainCancel()
	if err := server.WaitDrained(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDrained while OnError is blocked = %v", err)
	}

	unblock()
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained after OnError release: %v", err)
	}
}

// TestRequestCloseFromSessionCallbacksDoesNotDeadlock verifies nested lifecycle callbacks.
func TestRequestCloseFromSessionCallbacksDoesNotDeadlock(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	var closedCallbacks atomic.Int32
	if err := server.SetOnNewSessionRegister(func(*AppSession) {
		server.RequestClose()
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnSessionClosed(func(*AppSession, string) {
		closedCallbacks.Add(1)
		server.RequestClose()
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	address, done := startUnmanagedTestServer(t, server)
	conn, err := net.DialTimeout("tcp", address.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitUnmanagedTestServer(t, done)
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained: %v", err)
	}
	if closedCallbacks.Load() != 1 {
		t.Fatalf("closed callbacks = %d", closedCallbacks.Load())
	}
}

// TestRequestCloseFromManyCloseCallbacksDoesNotRecurse verifies one force-close owner.
func TestRequestCloseFromManyCloseCallbacksDoesNotRecurse(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0")
	var callbacks atomic.Int32
	if err := server.SetOnSessionClosed(func(*AppSession, string) {
		callbacks.Add(1)
		server.RequestClose()
	}); err != nil {
		t.Fatal(err)
	}
	server.ctx, server.cancel = context.WithCancel(context.Background())
	server.state = serverRunning
	const sessions = 128
	for index := 0; index < sessions; index++ {
		session := newTCPSession(server, fmt.Sprintf("session-%d", index), nil)
		if stored, created := server.admitSession(session); !created || stored != session {
			t.Fatalf("admit session %d = (%p, %v)", index, stored, created)
		}
	}
	server.RequestClose()
	server.startCleanup()
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained: %v", err)
	}
	if callbacks.Load() != sessions {
		t.Fatalf("closed callbacks = %d, want %d", callbacks.Load(), sessions)
	}
	if server.CountSessions() != 0 {
		t.Fatalf("active sessions = %d", server.CountSessions())
	}
	if len(server.allSessions()) != 0 {
		t.Fatalf("internal sessions after drain = %d", len(server.allSessions()))
	}
}

// TestCleanupFinalizesSessionWithClaimedCloseOwnership verifies drained cannot precede final session cleanup.
func TestCleanupFinalizesSessionWithClaimedCloseOwnership(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0")
	server.ctx, server.cancel = context.WithCancel(context.Background())
	server.state = serverRunning
	var callbacks atomic.Int32
	server.onSessionClosed = func(*AppSession, string) {
		callbacks.Add(1)
	}
	session := newTCPSession(server, "claimed-close", nil)
	if stored, created := server.admitSession(session); !created || stored != session {
		t.Fatalf("admit session = (%p, %v)", stored, created)
	}
	if !session.beginClose() {
		t.Fatal("session did not claim close ownership")
	}

	server.RequestClose()
	server.startCleanup()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.WaitDrained(ctx); err != nil {
		t.Fatalf("WaitDrained: %v", err)
	}
	if state := sessionState(session.state.Load()); state != sessionClosed {
		t.Fatalf("session state = %v, want closed", state)
	}
	if callbacks.Load() != 1 || server.Stats().ClosedSessions != 1 {
		t.Fatalf("close accounting: callbacks=%d stats=%d", callbacks.Load(), server.Stats().ClosedSessions)
	}
	if server.CountSessions() != 0 || len(server.allSessions()) != 0 {
		t.Fatalf("sessions after drain: active=%d internal=%d", server.CountSessions(), len(server.allSessions()))
	}
}

// TestTCPServerShutdownSendsEOF verifies graceful shutdown framing.
func TestTCPServerShutdownSendsEOF(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	if err := server.SetEOF([]byte("EOF")); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	waitForSessionCount(t, server, 1)
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 3)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "EOF" {
		t.Fatalf("EOF = %q", response)
	}
}

// TestShutdownDeadlineBoundsBlockedEOF verifies context bounds the whole shutdown path.
func TestShutdownDeadlineBoundsBlockedEOF(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithShutdownWriteTimeout(5*time.Second))
	if err := server.SetEOF(make([]byte, 64*1024*1024)); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForSessionCount(t, server, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = server.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("Shutdown took %v", elapsed)
	}
}

// TestShutdownTracksBlockedCloseFilterUntilDrain verifies EOF close tasks remain owned after timeout.
func TestShutdownTracksBlockedCloseFilterUntilDrain(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithShutdownWriteTimeout(20*time.Millisecond))
	filterStarted := make(chan struct{})
	releaseFilter := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseFilter) }) }
	defer unblock()
	if err := server.SetEOF([]byte("EOF")); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterSendPacketFilter(Middlewares{
		func(_ context.Context, _ *AppSession, data []byte) ([]byte, error) {
			startOnce.Do(func() { close(filterStarted) })
			<-releaseFilter
			return data, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr, serveDone := startUnmanagedTestServer(t, server)
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForSessionCount(t, server, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v", err)
	}
	select {
	case <-filterStarted:
	default:
		t.Fatal("EOF send filter did not start")
	}
	waitUnmanagedTestServer(t, serveDone)
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer drainCancel()
	if err := server.WaitDrained(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDrained while EOF filter is blocked = %v", err)
	}

	unblock()
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained after EOF filter release: %v", err)
	}
}

// TestCloseTracksBlockedResponseFilterUntilDrain verifies immediate close cannot abandon owned user code.
func TestCloseTracksBlockedResponseFilterUntilDrain(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	filterStarted := make(chan struct{})
	releaseFilter := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseFilter) }) }
	defer unblock()
	if err := server.RegisterSendPacketFilter(Middlewares{
		func(_ context.Context, _ *AppSession, data []byte) ([]byte, error) {
			startOnce.Do(func() { close(filterStarted) })
			<-releaseFilter
			return data, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return []byte("response"), nil
	}); err != nil {
		t.Fatal(err)
	}
	address, serveDone := startUnmanagedTestServer(t, server)
	conn, err := net.DialTimeout("tcp", address.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("request\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-filterStarted:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response send filter")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- server.Close() }()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked response filter retained Serve")
	}
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while response filter was blocked: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer drainCancel()
	if err := server.WaitDrained(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDrained while response filter is blocked = %v", err)
	}

	unblock()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close after response filter release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after response filter release")
	}
	if len(server.allSessions()) != 0 {
		t.Fatalf("internal sessions after Close = %d", len(server.allSessions()))
	}
}

// TestRequestCloseCancelsGracefulEOF verifies immediate close upgrades an active graceful cleanup.
func TestRequestCloseCancelsGracefulEOF(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithShutdownWriteTimeout(5*time.Second))
	filterStarted := make(chan struct{})
	filterCanceled := make(chan error, 1)
	releaseFilter := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseFilter) }) }
	defer unblock()
	if err := server.SetEOF([]byte("EOF")); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterSendPacketFilter(Middlewares{
		func(ctx context.Context, _ *AppSession, data []byte) ([]byte, error) {
			startOnce.Do(func() { close(filterStarted) })
			select {
			case <-ctx.Done():
				filterCanceled <- ctx.Err()
				return nil, ctx.Err()
			case <-releaseFilter:
				return data, nil
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	address, serveDone := startUnmanagedTestServer(t, server)
	conn, err := net.DialTimeout("tcp", address.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForSessionCount(t, server, 1)

	server.RequestShutdown()
	select {
	case <-filterStarted:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for graceful EOF filter")
	}
	server.RequestClose()
	select {
	case err := <-filterCanceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("EOF filter cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("immediate close did not cancel graceful EOF")
	}
	waitUnmanagedTestServer(t, serveDone)
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained: %v", err)
	}
}

// TestShutdownDeadlineLetsServeExitBeforeDrain verifies blocked TCP user code cannot retain Serve.
func TestShutdownDeadlineLetsServeExitBeforeDrain(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	filterStarted := make(chan struct{})
	releaseFilter := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseFilter) }) }
	defer unblock()
	if err := server.RegisterConnectionFilterTCP(func(net.Conn) error {
		close(filterStarted)
		<-releaseFilter
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr, serveDone := startUnmanagedTestServer(t, server)
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-filterStarted:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for TCP connection filter")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v", err)
	}
	waitUnmanagedTestServer(t, serveDone)
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer drainCancel()
	if err := server.WaitDrained(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDrained while filter is blocked = %v", err)
	}

	unblock()
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained after filter release: %v", err)
	}
}

// TestUDPInlineShutdownLetsServeExitBeforeDrain verifies inline actions run outside the UDP read loop.
func TestUDPInlineShutdownLetsServeExitBeforeDrain(t *testing.T) {
	server := mustNewUDPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithUDPDispatchMode(UDPDispatchInline))
	actionStarted := make(chan struct{})
	releaseAction := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseAction) }) }
	defer unblock()
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		startOnce.Do(func() { close(actionStarted) })
		<-releaseAction
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr, serveDone := startUnmanagedTestServer(t, server)
	conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("block\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-actionStarted:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for UDP inline action")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v", err)
	}
	waitUnmanagedTestServer(t, serveDone)
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer drainCancel()
	if err := server.WaitDrained(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDrained while action is blocked = %v", err)
	}

	unblock()
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained after action release: %v", err)
	}
}

// TestTCPShutdownRejectsLateSession verifies a blocked filter cannot admit after stopping.
func TestTCPShutdownRejectsLateSession(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	filterStarted := make(chan struct{})
	releaseFilter := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseFilter) }) }
	defer unblock()
	if err := server.RegisterConnectionFilterTCP(func(net.Conn) error {
		close(filterStarted)
		<-releaseFilter
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newSession := make(chan struct{}, 1)
	if err := server.SetOnNewSessionRegister(func(*AppSession) { newSession <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	addr, done := startUnmanagedTestServer(t, server)
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-filterStarted:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for TCP connection filter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v", err)
	}
	unblock()
	waitUnmanagedTestServer(t, done)
	if err := server.WaitDrained(context.Background()); err != nil {
		t.Fatalf("WaitDrained: %v", err)
	}
	select {
	case <-newSession:
		t.Fatal("TCP session admitted after shutdown started")
	default:
	}
	if server.CountSessions() != 0 {
		t.Fatalf("sessions after shutdown = %d", server.CountSessions())
	}
	if len(server.allSessions()) != 0 {
		t.Fatalf("internal sessions after shutdown = %d", len(server.allSessions()))
	}
	if server.Stats().AcceptedConnections != 0 {
		t.Fatalf("rejected TCP connection counted as accepted: %d", server.Stats().AcceptedConnections)
	}
}

// TestUDPShutdownRejectsLateSession verifies both dispatch modes reject late endpoints.
func TestUDPShutdownRejectsLateSession(t *testing.T) {
	for _, test := range []struct {
		name string
		mode UDPDispatchMode
	}{
		{name: "queued", mode: UDPDispatchQueued},
		{name: "inline", mode: UDPDispatchInline},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := mustNewUDPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithUDPDispatchMode(test.mode))
			filterStarted := make(chan struct{})
			releaseFilter := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(releaseFilter) }) }
			defer unblock()
			if err := server.RegisterConnectionFilterUDP(func(*net.UDPAddr) error {
				close(filterStarted)
				<-releaseFilter
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			newSession := make(chan *AppSession, 1)
			if err := server.SetOnNewSessionRegister(func(session *AppSession) { newSession <- session }); err != nil {
				t.Fatal(err)
			}
			if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) { return nil, nil }); err != nil {
				t.Fatal(err)
			}
			addr, done := startUnmanagedTestServer(t, server)
			conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.Write([]byte("x\n")); err != nil {
				t.Fatal(err)
			}
			select {
			case <-filterStarted:
			case <-time.After(time.Second):
				t.Fatal("timeout waiting for UDP connection filter")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Shutdown error = %v", err)
			}
			unblock()
			waitUnmanagedTestServer(t, done)
			if err := server.WaitDrained(context.Background()); err != nil {
				t.Fatalf("WaitDrained: %v", err)
			}
			select {
			case session := <-newSession:
				t.Fatalf("UDP session admitted after shutdown: closed=%v", session.Closed())
			default:
			}
			if server.CountSessions() != 0 {
				t.Fatalf("sessions after shutdown = %d", server.CountSessions())
			}
			if len(server.allSessions()) != 0 {
				t.Fatalf("internal sessions after shutdown = %d", len(server.allSessions()))
			}
		})
	}
}

// TestTLSHandshakePrecedesAdmission verifies invalid TLS peers never become sessions.
func TestTLSHandshakePrecedesAdmission(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	tlsConfig := certificateServer.TLS.Clone()
	certificateServer.Close()
	server := mustNewTCPServer(t, "127.0.0.1:0",
		WithIdleTimeout(0),
		WithTLS(tlsConfig),
		WithTLSHandshakeTimeout(time.Second),
		WithLogger(logger),
	)
	var filterCalls atomic.Int32
	if err := server.RegisterConnectionFilterTCP(func(net.Conn) error {
		filterCalls.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newSession := make(chan struct{}, 1)
	if err := server.SetOnNewSessionRegister(func(*AppSession) { newSession <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	handshakeError := make(chan error, 1)
	if err := server.SetOnError(func(err error) { handshakeError <- err }); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("not a TLS record")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-handshakeError:
		if !strings.Contains(err.Error(), "TLS handshake") {
			t.Fatalf("handshake error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for TLS handshake error")
	}
	if filterCalls.Load() != 0 {
		t.Fatalf("connection filter called before TLS authentication: %d", filterCalls.Load())
	}
	select {
	case <-newSession:
		t.Fatal("invalid TLS peer became a session")
	default:
	}
	if server.CountSessions() != 0 {
		t.Fatalf("sessions after failed handshake = %d", server.CountSessions())
	}
	if server.Stats().AcceptedConnections != 0 {
		t.Fatalf("failed TLS handshake counted as accepted: %d", server.Stats().AcceptedConnections)
	}
}

// TestTCPRejectedFilterDoesNotCountAcceptedConnection verifies statistics use admission semantics.
func TestTCPRejectedFilterDoesNotCountAcceptedConnection(t *testing.T) {
	server := mustNewTCPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	filterCalled := make(chan struct{}, 1)
	if err := server.RegisterConnectionFilterTCP(func(net.Conn) error {
		filterCalled <- struct{}{}
		return errors.New("rejected")
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	address := startTestServer(t, server)
	conn, err := net.DialTimeout("tcp", address.String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-filterCalled:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for connection filter")
	}
	if server.Stats().AcceptedConnections != 0 {
		t.Fatalf("filtered connection counted as accepted: %d", server.Stats().AcceptedConnections)
	}
	if server.CountSessions() != 0 {
		t.Fatalf("filtered connection created %d sessions", server.CountSessions())
	}
}

// TestUDPFinalTokenSplit verifies standard Scanner final-token semantics in both modes.
func TestUDPFinalTokenSplit(t *testing.T) {
	for _, test := range []struct {
		name string
		mode UDPDispatchMode
	}{
		{name: "queued", mode: UDPDispatchQueued},
		{name: "inline", mode: UDPDispatchInline},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := mustNewUDPServer(t, "127.0.0.1:0",
				WithIdleTimeout(0),
				WithUDPDispatchMode(test.mode),
			)
			if err := server.SetSplitFunc(func(data []byte, _ bool) (int, []byte, error) {
				if len(data) < 4 {
					return 0, nil, nil
				}
				return 0, data[:4], bufio.ErrFinalToken
			}); err != nil {
				t.Fatal(err)
			}
			if err := server.SetOnMessage(func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
				return token, nil
			}); err != nil {
				t.Fatal(err)
			}
			address := startTestServer(t, server)
			conn, err := net.DialUDP("udp", nil, address.(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write([]byte("done ignored")); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, 4)
			if _, err := io.ReadFull(conn, response); err != nil {
				t.Fatal(err)
			}
			if string(response) != "done" {
				t.Fatalf("response = %q", response)
			}
			if server.Stats().ProtocolErrors != 0 {
				t.Fatalf("protocol errors = %d", server.Stats().ProtocolErrors)
			}
		})
	}
}

// TestUDPEmptyDatagramDispatchesEmptyToken verifies zero-length datagrams are visible to ordinary actions.
func TestUDPEmptyDatagramDispatchesEmptyToken(t *testing.T) {
	for _, test := range []struct {
		name string
		mode UDPDispatchMode
	}{
		{name: "queued", mode: UDPDispatchQueued},
		{name: "inline", mode: UDPDispatchInline},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := mustNewUDPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithUDPDispatchMode(test.mode))
			tokenLengths := make(chan int, 1)
			if err := server.SetOnMessage(func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
				tokenLengths <- len(token)
				return []byte("empty"), nil
			}); err != nil {
				t.Fatal(err)
			}
			addr := startTestServer(t, server)
			conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write([]byte{}); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, len("empty"))
			if _, err := io.ReadFull(conn, response); err != nil {
				t.Fatal(err)
			}
			if string(response) != "empty" {
				t.Fatalf("response = %q", response)
			}
			if tokenLength := <-tokenLengths; tokenLength != 0 {
				t.Fatalf("empty datagram token length = %d", tokenLength)
			}
		})
	}
}

// TestUDPEmptyDatagramStillHonorsReceiveProtocol verifies routed protocols reject an empty token.
func TestUDPEmptyDatagramStillHonorsReceiveProtocol(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mustNewUDPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithLogger(logger))
	if err := server.SetReceiveFilter(&filter.FixedHeaderReceiveFilter{}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for server.Stats().ProtocolErrors == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if server.Stats().ProtocolErrors != 1 {
		t.Fatalf("protocol errors = %d, want 1", server.Stats().ProtocolErrors)
	}
}

// TestUDPBeginEndRouteIntegration verifies strict one-datagram route dispatch.
func TestUDPBeginEndRouteIntegration(t *testing.T) {
	for _, test := range []struct {
		name string
		mode UDPDispatchMode
	}{
		{name: "queued", mode: UDPDispatchQueued},
		{name: "inline", mode: UDPDispatchInline},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := mustNewUDPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithUDPDispatchMode(test.mode))
			receiveFilter := &filter.BeginEndMarkReceiveFilter{Begin: []byte("!$"), End: []byte("$!")}
			if err := server.SetReceiveFilter(receiveFilter); err != nil {
				t.Fatal(err)
			}
			if err := server.Action("/say", func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
				return token, nil
			}); err != nil {
				t.Fatal(err)
			}
			addr := startTestServer(t, server)
			conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			packet := beginEndActionPacket(receiveFilter.Begin, receiveFilter.End, "/say", []byte("hello"))
			if _, err := conn.Write(packet); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, 5)
			if _, err := io.ReadFull(conn, response); err != nil {
				t.Fatal(err)
			}
			if string(response) != "hello" {
				t.Fatalf("response = %q", response)
			}
			waitForSessionCount(t, server, 1)
			stats := server.Stats()
			if stats.PacketsRead != 1 || stats.BytesRead != uint64(len(packet)) || stats.BytesWritten != uint64(len(response)) {
				t.Fatalf("stats = %+v", stats)
			}
		})
	}
}

// TestUDPActionPanicIsolation verifies one bad datagram does not terminate its worker.
func TestUDPActionPanicIsolation(t *testing.T) {
	for _, test := range []struct {
		name string
		mode UDPDispatchMode
	}{
		{name: "queued", mode: UDPDispatchQueued},
		{name: "inline", mode: UDPDispatchInline},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			server := mustNewUDPServer(t, "127.0.0.1:0", WithIdleTimeout(0), WithLogger(logger), WithUDPDispatchMode(test.mode))
			errorsSeen := make(chan error, 1)
			if err := server.SetOnError(func(err error) {
				select {
				case errorsSeen <- err:
				default:
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := server.SetOnMessage(func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
				if string(token) == "panic" {
					panic("test action")
				}
				return token, nil
			}); err != nil {
				t.Fatal(err)
			}
			addr := startTestServer(t, server)
			conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write([]byte("panic")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-errorsSeen:
				if got := err.Error(); !strings.Contains(got, "UDP handler panic") {
					t.Fatalf("panic error = %q", got)
				}
			case <-time.After(time.Second):
				t.Fatal("timeout waiting for UDP panic error")
			}

			if _, err := conn.Write([]byte("ok")); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, 2)
			if _, err := io.ReadFull(conn, response); err != nil {
				t.Fatal(err)
			}
			if string(response) != "ok" {
				t.Fatalf("response after panic = %q", response)
			}
		})
	}
}

// TestUDPLargeDatagramIsNotTruncated verifies a datagram larger than the old 4 KiB limit.
func TestUDPLargeDatagramIsNotTruncated(t *testing.T) {
	server := mustNewUDPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	if err := server.SetSplitFunc(func(data []byte, _ bool) (int, []byte, error) {
		if len(data) == 0 {
			return 0, nil, nil
		}
		return len(data), data, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
		return []byte(strconv.Itoa(len(token))), nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write(make([]byte, 6000)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 16)
	size, err := conn.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(response[:size]) != "6000" {
		t.Fatalf("received size = %q", response[:size])
	}
}

// TestUDPSessionReuseAndFind verifies one endpoint maps to one synchronous session.
func TestUDPSessionReuseAndFind(t *testing.T) {
	server := mustNewUDPServer(t, "127.0.0.1:0", WithIdleTimeout(0))
	var registrations atomic.Int32
	if err := server.SetOnNewSessionRegister(func(session *AppSession) {
		registrations.Add(1)
		session.SetAttr("kind", "udp")
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("first\n"))
	_, _ = conn.Write([]byte("second\n"))
	waitForSessionCount(t, server, 1)
	if registrations.Load() != 1 {
		t.Fatalf("registrations = %d", registrations.Load())
	}
	found := server.FindSessions(func(attrs map[string]any) bool { return attrs["kind"] == "udp" })
	if len(found) != 1 {
		t.Fatalf("found sessions = %d", len(found))
	}
}

// TestUDPIdleTimeoutClosesSession verifies the centralized idle sweeper.
func TestUDPIdleTimeoutClosesSession(t *testing.T) {
	for _, test := range []struct {
		name string
		mode UDPDispatchMode
	}{
		{name: "queued", mode: UDPDispatchQueued},
		{name: "inline", mode: UDPDispatchInline},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := mustNewUDPServer(t, "127.0.0.1:0", WithIdleTimeout(20*time.Millisecond), WithUDPDispatchMode(test.mode))
			closed := make(chan string, 1)
			if err := server.SetOnSessionClosed(func(_ *AppSession, reason string) { closed <- reason }); err != nil {
				t.Fatal(err)
			}
			if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) { return nil, nil }); err != nil {
				t.Fatal(err)
			}
			addr := startTestServer(t, server)
			conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_, _ = conn.Write([]byte("idle\n"))
			waitForSessionCount(t, server, 1)
			select {
			case reason := <-closed:
				if reason != "UDP idle timeout" {
					t.Fatalf("close reason = %q", reason)
				}
			case <-time.After(time.Second):
				t.Fatal("timeout waiting for idle close")
			}
		})
	}
}

// TestUDPDatagramLimitAndQueueBackpressure verifies bounded rejection paths.
func TestUDPDatagramLimitAndQueueBackpressure(t *testing.T) {
	server := mustNewUDPServer(t, "127.0.0.1:0",
		WithIdleTimeout(0),
		WithMaxDatagramSize(8),
		WithUDPWorkers(1),
		WithUDPQueueSize(1),
	)
	blocked := make(chan struct{})
	started := make(chan struct{}, 1)
	if err := server.SetOnMessage(func(context.Context, *AppSession, []byte) ([]byte, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-blocked
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write(make([]byte, 16))
	deadline := time.Now().Add(time.Second)
	for server.Stats().PacketsDropped == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if server.Stats().PacketsDropped == 0 {
		t.Fatal("oversized datagram was not dropped")
	}
	_, _ = conn.Write([]byte("a\n"))
	<-started
	for index := 0; index < 20; index++ {
		_, _ = conn.Write([]byte("b\n"))
	}
	deadline = time.Now().Add(time.Second)
	for server.Stats().PacketsDropped < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(blocked)
	if server.Stats().PacketsDropped < 2 {
		t.Fatalf("queue drops = %d", server.Stats().PacketsDropped)
	}
}

// TestUDPQueuedBatchBoundary verifies queued dispatch does not lose a prefetched packet.
func TestUDPQueuedBatchBoundary(t *testing.T) {
	server := mustNewUDPServer(t, "127.0.0.1:0",
		WithIdleTimeout(0),
		WithUDPWorkers(1),
		WithUDPQueueSize(128),
		WithUDPDispatchMode(UDPDispatchQueued),
	)
	release := make(chan struct{})
	started := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	if err := server.SetOnMessage(func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return token, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := startTestServer(t, server)
	conn, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for first queued action")
	}
	const packets = 100
	for index := 1; index < packets; index++ {
		if _, err := conn.Write([]byte("x\n")); err != nil {
			t.Fatal(err)
		}
	}
	waitForPacketCount(t, server, packets)
	unblock()
	buffer := make([]byte, 1)
	for index := 0; index < packets; index++ {
		if _, err := conn.Read(buffer); err != nil {
			t.Fatalf("read response %d: %v", index, err)
		}
	}
}

// TestCallbackPanicIsolation verifies user callback panics become operational errors.
func TestCallbackPanicIsolation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mustNewTCPServer(t, "127.0.0.1:0", WithLogger(logger))
	var errorsSeen atomic.Int32
	server.onError = func(error) { errorsSeen.Add(1) }
	server.onNewSessionRegister = func(*AppSession) { panic("new") }
	server.onSessionClosed = func(*AppSession, string) { panic("closed") }
	session := &AppSession{}
	server.safeNewSession(session)
	server.safeSessionClosed(session, "test")
	server.notifyErrorCallback(ErrUDPQueueFull)
	if errorsSeen.Load() != 3 {
		t.Fatalf("errors seen = %d", errorsSeen.Load())
	}
}

// mustNewTCPServer constructs a test TCP server.
func mustNewTCPServer(t *testing.T, address string, options ...Option) *Server {
	t.Helper()
	server, err := NewTCP(address, options...)
	if err != nil {
		t.Fatalf("NewTCP: %v", err)
	}
	return server
}

// mustNewUDPServer constructs a test UDP server.
func mustNewUDPServer(t *testing.T, address string, options ...Option) *Server {
	t.Helper()
	server, err := NewUDP(address, options...)
	if err != nil {
		t.Fatalf("NewUDP: %v", err)
	}
	return server
}

// startTestServer serves in the background and registers deterministic cleanup.
func startTestServer(t *testing.T, server *Server) net.Addr {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if addr := server.Addr(); addr != nil {
			return addr
		}
		select {
		case err := <-done:
			done <- err
			t.Fatalf("Serve exited before binding: %v", err)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timeout waiting for server address")
	return nil
}

// startUnmanagedTestServer starts a server whose completion is owned by the caller.
func startUnmanagedTestServer(t *testing.T, server *Server) (net.Addr, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if addr := server.Addr(); addr != nil {
			return addr, done
		}
		select {
		case err := <-done:
			t.Fatalf("Serve exited before binding: %v", err)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timeout waiting for server address")
	return nil, done
}

// waitUnmanagedTestServer waits for a caller-owned Serve invocation.
func waitUnmanagedTestServer(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}

// waitForSessionCount waits for asynchronous connection handling.
func waitForSessionCount(t *testing.T, server *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if server.CountSessions() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session count = %d, want %d", server.CountSessions(), want)
}

// waitForPacketCount waits for UDP read accounting to reach a target.
func waitForPacketCount(t *testing.T, server *Server, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if server.Stats().PacketsRead >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("packets read = %d, want at least %d", server.Stats().PacketsRead, want)
}

// beginEndActionPacket constructs a complete routed begin/end packet.
func beginEndActionPacket(begin, end []byte, action string, body []byte) []byte {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(action)))
	packet := make([]byte, 0, len(begin)+4+len(action)+len(body)+len(end))
	packet = append(packet, begin...)
	packet = append(packet, header...)
	packet = append(packet, action...)
	packet = append(packet, body...)
	return append(packet, end...)
}
