package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"

	goserver "github.com/zboyco/go-server"
)

// runTCPBeginEndDemo 演示 TCP + BeginEnd 协议下的核心能力：
// 路由、模块注册、全局/模块中间件、连接过滤、发送过滤、会话属性、
// 会话查找、EOF、关闭流程、客户端收发。
func runTCPBeginEndDemo() result {
	return runDemo("tcp begin-end full features", func(t *demoT) {
		recvFilter := beginEndFilter()
		server := newTCPServer(t, "127.0.0.1", 0,
			goserver.WithIdleTimeout(5*time.Second),
			goserver.WithMaxFrameSize(64*1024),
		)

		var (
			newSessionCount    int32
			closedSessionCount int32
			filteredConnCount  int32
			closedReasonCh     = make(chan string, 1)
		)

		must(t, server.SetEOF([]byte("IO_EOF")))
		must(t, server.SetReceiveFilter(recvFilter))
		must(t, server.SetOnError(func(err error) {
			log.Printf("[tcp-begin-end] onError: %v", err)
		}))
		must(t, server.SetOnNewSessionRegister(func(session *goserver.AppSession) {
			atomic.AddInt32(&newSessionCount, 1)
			session.SetAttr("role", "demo")
			session.SetAttr("from", session.ID)
			log.Printf("[tcp-begin-end] new session: %s", session.ID)
		}))
		must(t, server.SetOnSessionClosed(func(session *goserver.AppSession, reason string) {
			atomic.AddInt32(&closedSessionCount, 1)
			select {
			case closedReasonCh <- reason:
			default:
			}
			log.Printf("[tcp-begin-end] session closed: %s reason=%s", session.ID, reason)
		}))
		must(t, server.RegisterConnectionFilterTCP(func(conn net.Conn) error {
			atomic.AddInt32(&filteredConnCount, 1)
			if conn.RemoteAddr() == nil {
				return fmt.Errorf("missing remote addr")
			}
			return nil
		}))
		// 发送过滤器：响应统一包上 Begin/End 外壳，方便客户端按协议拆包。
		must(t, server.RegisterSendPacketFilter(goserver.Middlewares{
			func(_ actionContext, _ *goserver.AppSession, buf []byte) ([]byte, error) {
				return wrapBeginEnd(recvFilter, buf), nil
			},
		}))
		must(t, server.RegisterBeforeMiddlewares(goserver.Middlewares{
			func(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
				return append([]byte("G1:"), token...), nil
			},
		}))
		must(t, server.RegisterAfterMiddlewares(goserver.Middlewares{
			func(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
				return append(token, []byte(":G2")...), nil
			},
		}))

		// 单 Action 注册；路径大小写会被统一成小写。
		must(t, server.Action("/Echo", func(_ actionContext, session *goserver.AppSession, token []byte) ([]byte, error) {
			return []byte(fmt.Sprintf("echo(%s)=%s", session.ID[:8], string(token))), nil
		}))
		must(t, server.RegisterModule(&demoModule{}))
		must(t, server.RegisterModule(&otherModule{suffix: "#v2"}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)
		log.Printf("[tcp-begin-end] listen %s", addr.String())

		cli := newBeginEndClient(t, goserver.TCP, host, port, recvFilter)
		must(t, cli.Connect(backgroundContext))
		defer cli.Close()
		must(t, cli.Conn().SetDeadline(time.Now().Add(5*time.Second)))

		// 1) 单 Action + 全局中间件
		// before: G1:ping -> action: echo(id)=G1:ping -> after: echo(id)=G1:ping:G2
		must(t, cli.SendAction(backgroundContext, "/echo", []byte("ping")))
		got, err := cli.Receive(backgroundContext)
		must(t, err)
		if !bytes.Contains(got, []byte("echo(")) || !bytes.Contains(got, []byte("G1:ping:G2")) {
			t.Fatalf("echo response = %q, want contain echo(...) and G1:ping:G2", string(got))
		}

		// 2) 模块路由 /say
		must(t, cli.SendAction(backgroundContext, "/say", []byte("hello")))
		got, err = cli.Receive(backgroundContext)
		must(t, err)
		if string(got) != "G1:hello:G2" {
			t.Fatalf("say response = %q, want %q", string(got), "G1:hello:G2")
		}

		// 3) 模块路由 + 模块级中间件 /v2/print
		// 执行顺序：全局 before -> 模块 before -> action -> 模块 after -> 全局 after
		// 由于 before 使用前缀拼接，后执行的模块 before 会出现在字符串更前面：
		// G1:mod -> M1:G1:mod -> M1:G1:mod#v2 -> M1:G1:mod#v2:M2 -> M1:G1:mod#v2:M2:G2
		must(t, cli.SendAction(backgroundContext, "/v2/print", []byte("mod")))
		got, err = cli.Receive(backgroundContext)
		must(t, err)
		wantPrint := "M1:G1:mod#v2:M2:G2"
		if string(got) != wantPrint {
			t.Fatalf("print response = %q, want %q", string(got), wantPrint)
		}

		waitSessionCount(t, server, 1)
		if atomic.LoadInt32(&newSessionCount) != 1 {
			t.Fatalf("new session count = %d, want 1", newSessionCount)
		}
		if atomic.LoadInt32(&filteredConnCount) != 1 {
			t.Fatalf("connection filter count = %d, want 1", filteredConnCount)
		}

		// 4) 会话属性与查找
		var session *goserver.AppSession
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			for _, item := range server.Sessions() {
				session = item
				break
			}
			if session != nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if session == nil {
			t.Fatalf("Sessions returned no session")
		}
		role, err := session.GetAttr("role")
		must(t, err)
		if role != "demo" {
			t.Fatalf("role attr = %v, want demo", role)
		}
		foundByID, err := server.GetSessionByID(session.ID)
		must(t, err)
		if foundByID.ID != session.ID {
			t.Fatalf("GetSessionByID mismatch")
		}
		foundByAttr := false
		for _, item := range server.FindSessions(func(attrs map[string]any) bool {
			return attrs["role"] == "demo"
		}) {
			if item.ID == session.ID {
				foundByAttr = true
			}
		}
		if !foundByAttr {
			t.Fatalf("FindSessions did not find session")
		}

		// 5) 主动发送到会话（不经过 action）
		must(t, session.Send(backgroundContext, []byte("push-message")))
		got, err = cli.Receive(backgroundContext)
		must(t, err)
		if string(got) != "push-message" {
			t.Fatalf("push message = %q, want %q", string(got), "push-message")
		}

		// 6) Shutdown 关闭会话并发送 EOF（经过发送过滤器后仍按 Begin/End 拆出 IO_EOF）
		must(t, server.Shutdown(backgroundContext))
		waitServerExit(t, done)
		waitSessionCount(t, server, 0)

		must(t, cli.Conn().SetDeadline(time.Now().Add(2*time.Second)))
		got, err = cli.Receive(backgroundContext)
		must(t, err)
		if string(got) != "IO_EOF" {
			t.Fatalf("EOF = %q, want %q", string(got), "IO_EOF")
		}

		// 连接随后应结束；不同 OS 错误文案可能不同。
		must(t, cli.Conn().SetDeadline(time.Now().Add(time.Second)))
		_, err = cli.Receive(backgroundContext)
		if err == nil {
			t.Fatalf("expected connection close after Shutdown")
		}
		log.Printf("[tcp-begin-end] post-stop read err: %v", err)

		select {
		case reason := <-closedReasonCh:
			if reason == "" {
				t.Fatalf("closed reason is empty")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for closed session callback")
		}
		if atomic.LoadInt32(&closedSessionCount) != 1 {
			t.Fatalf("closed session count = %d, want 1", closedSessionCount)
		}

		// 服务停止后 Addr 应返回 nil
		deadline = time.Now().Add(time.Second)
		for time.Now().Before(deadline) && server.Addr() != nil {
			time.Sleep(5 * time.Millisecond)
		}
		if server.Addr() != nil {
			t.Fatalf("Addr after Shutdown should be nil")
		}

	})
}

// demoModule 演示 ActionModule + Summary。
type demoModule struct{}

// Root 返回模块路由前缀。
func (m *demoModule) Root() string { return "/" }

// Summary 返回模块摘要，启动时会打印到路由表。
func (m *demoModule) Summary() string { return "demo module" }

// Say 回显消息。
func (m *demoModule) Say(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
	return token, nil
}

// otherModule 演示模块级前后置中间件。
type otherModule struct {
	suffix string
}

// Root 返回模块路由前缀。
func (m *otherModule) Root() string { return "/v2" }

// Print 追加模块自定义后缀。
func (m *otherModule) Print(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
	return append(token, []byte(m.suffix)...), nil
}

// MiddlewaresBeforeAction 返回模块前置中间件。
func (m *otherModule) MiddlewaresBeforeAction() goserver.Middlewares {
	return goserver.Middlewares{
		func(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
			return append([]byte("M1:"), token...), nil
		},
	}
}

// MiddlewaresAfterAction 返回模块后置中间件。
func (m *otherModule) MiddlewaresAfterAction() goserver.Middlewares {
	return goserver.Middlewares{
		func(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
			return append(token, []byte(":M2")...), nil
		},
	}
}
