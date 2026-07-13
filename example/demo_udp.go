package main

import (
	"bytes"
	"log"
	"sync/atomic"
	"time"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/client"
)

// runUDPBeginEndDemo 演示 UDP BeginEnd 路由，以及同一远端地址会话复用。
func runUDPBeginEndDemo() result {
	return runDemo("udp begin-end session reuse", func(t *demoT) {
		recvFilter := beginEndFilter()
		server := goserver.NewUDP("127.0.0.1", 0)
		server.IdleSessionTimeOut = 3

		var newSessions int32
		must(t, server.SetReceiveFilter(recvFilter))
		must(t, server.SetOnNewSessionRegister(func(session *goserver.AppSession) {
			atomic.AddInt32(&newSessions, 1)
			session.SetAttr("proto", "udp")
			log.Printf("[udp] new session %s", session.ID)
		}))
		must(t, server.RegisterSendPacketFilter(goserver.Middlewares{
			func(_ *goserver.AppSession, buf []byte) ([]byte, error) {
				return wrapBeginEnd(recvFilter, buf), nil
			},
		}))
		must(t, server.Action("/say", func(_ *goserver.AppSession, token []byte) ([]byte, error) {
			return append([]byte("UDP:"), token...), nil
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)

		cli := client.NewBeginEndMarkClient(goserver.UDP, host, port, recvFilter)
		cli.SetScannerSplitFunc(recvFilter.SplitFunc())
		must(t, cli.Connect())
		defer cli.Close()
		must(t, cli.GetRawConn().SetDeadline(time.Now().Add(5*time.Second)))

		// 连续发送两个包，应复用同一 UDP 会话。
		must(t, cli.SendAction("/say", []byte("one")))
		got, err := cli.Receive()
		must(t, err)
		if !bytes.Equal(got, []byte("UDP:one")) {
			t.Fatalf("first response = %q, want %q", string(got), "UDP:one")
		}

		must(t, cli.SendAction("/say", []byte("two")))
		got, err = cli.Receive()
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
		for session := range server.GetSessionByAttr(func(attrs map[string]interface{}) bool {
			return attrs["proto"] == "udp"
		}) {
			if session != nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("udp session attr lookup failed")
		}

		must(t, server.Stop())
		waitServerExit(t, done)
		waitSessionCount(t, server, 0)
	})
}
