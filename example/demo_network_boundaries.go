package main

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"

	goserver "github.com/zboyco/go-server/v2"
)

// runUDPConnectionFilterDemo 演示 UDP 连接过滤器拒绝首包并允许后续数据包。
func runUDPConnectionFilterDemo() result {
	return runDemo("udp connection filter", func(t *demoT) {
		var attempts int32
		var actionCalls int32
		rejected := errors.New("reject first udp packet")

		server := newUDPServer(t, "127.0.0.1", 0, goserver.WithIdleTimeout(0))
		must(t, server.RegisterConnectionFilterUDP(func(_ *net.UDPAddr) error {
			if atomic.AddInt32(&attempts, 1) == 1 {
				return rejected
			}
			return nil
		}))
		must(t, server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
			atomic.AddInt32(&actionCalls, 1)
			return append([]byte("ALLOWED:"), token...), nil
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)
		cli := newSimpleClient(t, goserver.UDP, host, port)
		must(t, cli.Connect(backgroundContext))
		defer cli.Close()
		must(t, cli.Conn().SetDeadline(time.Now().Add(4*time.Second)))

		must(t, cli.Send(backgroundContext, []byte("blocked\n")))
		waitCondition(t, time.Second, "first UDP filter invocation", func() bool {
			return atomic.LoadInt32(&attempts) >= 1
		})
		if got := server.CountSessions(); got != 0 {
			t.Fatalf("rejected UDP packet created %d session(s), want 0", got)
		}
		if got := atomic.LoadInt32(&actionCalls); got != 0 {
			t.Fatalf("rejected UDP packet invoked action %d time(s), want 0", got)
		}

		must(t, cli.Send(backgroundContext, []byte("accepted\n")))
		got, err := cli.Receive(backgroundContext)
		must(t, err)
		if string(got) != "ALLOWED:accepted" {
			t.Fatalf("UDP filter response = %q, want %q", string(got), "ALLOWED:accepted")
		}
		if calls := atomic.LoadInt32(&actionCalls); calls != 1 {
			t.Fatalf("allowed UDP action calls = %d, want 1", calls)
		}
		waitSessionCount(t, server, 1)

		must(t, server.Close())
		waitServerExit(t, done)
	})
}

// runIdleTimeoutDemo 演示 TCP 和 UDP 会话均会在空闲超时后自动关闭。
func runIdleTimeoutDemo() result {
	return runDemo("tcp and udp idle timeout", func(t *demoT) {
		runTCPIdleTimeoutScenario(t)
		runUDPIdleTimeoutScenario(t)
	})
}

// runTCPIdleTimeoutScenario 验证 TCP 读超时关闭会话并传递超时原因。
func runTCPIdleTimeoutScenario(t *demoT) {
	closed := make(chan string, 1)
	server := newTCPServer(t, "127.0.0.1", 0, goserver.WithIdleTimeout(time.Second))
	must(t, server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, _ []byte) ([]byte, error) {
		return nil, nil
	}))
	must(t, server.SetOnSessionClosed(func(_ *goserver.AppSession, reason string) {
		closed <- reason
	}))

	addr, done := startServer(t, server)
	host, port := hostPort(t, addr)
	cli := newSimpleClient(t, goserver.TCP, host, port)
	must(t, cli.Connect(backgroundContext))
	defer cli.Close()
	waitSessionCount(t, server, 1)

	select {
	case reason := <-closed:
		if !strings.Contains(reason, "timeout") {
			t.Fatalf("TCP idle close reason = %q, want timeout", reason)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("timeout waiting for TCP idle session close")
	}
	waitSessionCount(t, server, 0)
	must(t, server.Close())
	waitServerExit(t, done)
}

// runUDPIdleTimeoutScenario 验证 UDP 模拟会话会在空闲后关闭并从会话池移除。
func runUDPIdleTimeoutScenario(t *demoT) {
	closed := make(chan string, 1)
	server := newUDPServer(t, "127.0.0.1", 0, goserver.WithIdleTimeout(time.Second))
	must(t, server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, _ []byte) ([]byte, error) {
		return nil, nil
	}))
	must(t, server.SetOnSessionClosed(func(_ *goserver.AppSession, reason string) {
		closed <- reason
	}))

	addr, done := startServer(t, server)
	host, port := hostPort(t, addr)
	cli := newSimpleClient(t, goserver.UDP, host, port)
	must(t, cli.Connect(backgroundContext))
	defer cli.Close()
	must(t, cli.Send(backgroundContext, []byte("idle\n")))
	waitSessionCount(t, server, 1)

	select {
	case reason := <-closed:
		if !strings.Contains(reason, "timeout") {
			t.Fatalf("UDP idle close reason = %q, want timeout", reason)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("timeout waiting for UDP idle session close")
	}
	waitSessionCount(t, server, 0)
	must(t, server.Close())
	waitServerExit(t, done)
}

// runIPv6Demo 演示在 IPv6 回环地址上监听并完成 TCP 收发。
func runIPv6Demo() result {
	return runDemo("tcp ipv6 loopback", func(t *demoT) {
		probe, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}
		must(t, probe.Close())

		server := newTCPServer(t, "::1", 0, goserver.WithIdleTimeout(0))
		must(t, server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
			return append([]byte("IPV6:"), token...), nil
		}))
		addr, done := startServer(t, server)

		conn, err := net.DialTimeout("tcp6", addr.String(), 2*time.Second)
		must(t, err)
		defer conn.Close()
		must(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
		_, err = conn.Write([]byte("loopback\n"))
		must(t, err)

		want := []byte("IPV6:loopback")
		got := make([]byte, len(want))
		_, err = io.ReadFull(conn, got)
		must(t, err)
		if string(got) != string(want) {
			t.Fatalf("IPv6 response = %q, want %q", string(got), string(want))
		}

		must(t, server.Close())
		waitServerExit(t, done)
	})
}
