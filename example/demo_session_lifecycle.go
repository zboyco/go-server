package main

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	goserver "github.com/zboyco/go-server/v2"
)

// runSessionLifecycleDemo 演示会话属性、原始发送、幂等关闭和关闭后错误语义。
func runSessionLifecycleDemo() result {
	return runDemo("session lifecycle and attributes", func(t *demoT) {
		sessionCh := make(chan *goserver.AppSession, 1)
		closedCh := make(chan string, 1)
		var closedCalls int32

		server := newTCPServer(t, "127.0.0.1", 0, goserver.WithIdleTimeout(0))
		must(t, server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, _ []byte) ([]byte, error) {
			return nil, nil
		}))
		must(t, server.SetOnNewSessionRegister(func(session *goserver.AppSession) {
			sessionCh <- session
		}))
		must(t, server.SetOnSessionClosed(func(_ *goserver.AppSession, reason string) {
			atomic.AddInt32(&closedCalls, 1)
			closedCh <- reason
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)
		cli := newSimpleClient(t, goserver.TCP, host, port)
		must(t, cli.Connect(backgroundContext))
		defer cli.Close()
		must(t, cli.Conn().SetDeadline(time.Now().Add(3*time.Second)))

		var session *goserver.AppSession
		select {
		case session = <-sessionCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for new session")
		}
		waitSessionCount(t, server, 1)

		must(t, session.AddAttr("role", "reader"))
		if err := session.AddAttr("role", "duplicate"); err == nil {
			t.Fatalf("duplicate AddAttr should return an error")
		}
		session.SetAttr("role", "writer")
		role, err := session.GetAttr("role")
		must(t, err)
		if role != "writer" {
			t.Fatalf("role attr = %v, want writer", role)
		}
		must(t, session.DelAttr("role"))
		if _, err := session.GetAttr("role"); err == nil {
			t.Fatalf("GetAttr after DelAttr should return an error")
		}
		exerciseConcurrentSessionAttrs(t, session)

		must(t, session.SendRaw(backgroundContext, []byte("raw")))
		got := make([]byte, 3)
		_, err = io.ReadFull(cli.Conn(), got)
		must(t, err)
		if string(got) != "raw" {
			t.Fatalf("SendRaw response = %q, want raw", string(got))
		}

		must(t, session.Close("example close"))
		must(t, session.Close("ignored duplicate close"))
		select {
		case reason := <-closedCh:
			if reason != "example close" {
				t.Fatalf("session close reason = %q, want %q", reason, "example close")
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for session close callback")
		}
		if calls := atomic.LoadInt32(&closedCalls); calls != 1 {
			t.Fatalf("session close callback calls = %d, want 1", calls)
		}
		if !session.Closed() {
			t.Fatalf("session Closed = false after Close")
		}
		if err := session.Send(backgroundContext, []byte("after-close")); err == nil {
			t.Fatalf("Send after Close should return an error")
		}
		if err := session.SendRaw(backgroundContext, []byte("after-close")); err == nil {
			t.Fatalf("SendRaw after Close should return an error")
		}
		waitSessionCount(t, server, 0)

		must(t, server.Close())
		waitServerExit(t, done)
	})
}

// exerciseConcurrentSessionAttrs 并发执行属性增删改查，演示会话属性的线程安全用法。
func exerciseConcurrentSessionAttrs(t *demoT, session *goserver.AppSession) {
	t.Helper()

	const workers = 16
	errCh := make(chan error, workers*3)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			key := fmt.Sprintf("parallel-%d", index)
			if err := session.AddAttr(key, index); err != nil {
				errCh <- fmt.Errorf("AddAttr %s: %w", key, err)
				return
			}
			session.SetAttr("shared", index)
			if _, err := session.GetAttr("shared"); err != nil {
				errCh <- fmt.Errorf("GetAttr shared: %w", err)
			}
			if err := session.DelAttr(key); err != nil {
				errCh <- fmt.Errorf("DelAttr %s: %w", key, err)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent session attrs: %v", err)
	}
	must(t, session.DelAttr("shared"))
}
