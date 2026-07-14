package main

import (
	"bytes"
	"log"
	"sync/atomic"
	"time"

	goserver "github.com/zboyco/go-server"
)

// runUDPBeginEndDemo 演示 inline UDP BeginEnd 路由，以及同一远端地址会话复用。
func runUDPBeginEndDemo() result {
	return runDemo("udp inline begin-end session reuse", func(t *demoT) {
		recvFilter := beginEndFilter()
		server := newUDPServer(t, "127.0.0.1", 0,
			goserver.WithIdleTimeout(3*time.Second),
			goserver.WithUDPDispatchMode(goserver.UDPDispatchInline),
		)

		var newSessions int32
		must(t, server.SetReceiveFilter(recvFilter))
		must(t, server.SetOnNewSessionRegister(func(session *goserver.AppSession) {
			atomic.AddInt32(&newSessions, 1)
			session.SetAttr("proto", "udp")
			log.Printf("[udp] new session %s", session.ID)
		}))
		must(t, server.RegisterSendPacketFilter(goserver.Middlewares{
			func(_ actionContext, _ *goserver.AppSession, buf []byte) ([]byte, error) {
				return wrapBeginEnd(recvFilter, buf), nil
			},
		}))
		must(t, server.Action("/say", func(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
			return append([]byte("UDP:"), token...), nil
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)

		cli := newBeginEndClient(t, goserver.UDP, host, port, recvFilter)
		must(t, cli.Connect(backgroundContext))
		defer cli.Close()
		must(t, cli.Conn().SetDeadline(time.Now().Add(5*time.Second)))

		// 连续发送两个包，应复用同一 UDP 会话。
		must(t, cli.SendAction(backgroundContext, "/say", []byte("one")))
		got, err := cli.Receive(backgroundContext)
		must(t, err)
		if !bytes.Equal(got, []byte("UDP:one")) {
			t.Fatalf("first response = %q, want %q", string(got), "UDP:one")
		}

		must(t, cli.SendAction(backgroundContext, "/say", []byte("two")))
		got, err = cli.Receive(backgroundContext)
		must(t, err)
		if !bytes.Equal(got, []byte("UDP:two")) {
			t.Fatalf("second response = %q, want %q", string(got), "UDP:two")
		}

		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && atomic.LoadInt32(&newSessions) == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		if got := atomic.LoadInt32(&newSessions); got != 1 {
			t.Fatalf("udp new session count = %d, want 1", got)
		}
		if got := server.CountSessions(); got != 1 {
			t.Fatalf("udp session count = %d, want 1", got)
		}

		found := false
		for _, session := range server.FindSessions(func(attrs map[string]any) bool {
			return attrs["proto"] == "udp"
		}) {
			if session != nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("udp session attr lookup failed")
		}

		must(t, server.Close())
		waitServerExit(t, done)
		waitSessionCount(t, server, 0)
	})
}
