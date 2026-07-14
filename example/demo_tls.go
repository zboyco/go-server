package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	goserver "github.com/zboyco/go-server"
)

// runTLSDemo 演示 WithTLS 选项与 TLS 客户端通信。
func runTLSDemo() result {
	return runDemo("tcp tls on-message", func(t *demoT) {
		serverTLS, clientTLS := generateTLSConfig(t)
		server := newTCPServer(t, "127.0.0.1", 0, goserver.WithIdleTimeout(0), goserver.WithTLS(serverTLS))

		must(t, server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
			return append([]byte("TLS:"), token...), nil
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)
		log.Printf("[tls] listen %s", addr.String())

		dialer := &net.Dialer{Timeout: 2 * time.Second}
		conn, err := tls.DialWithDialer(dialer, "tcp", fmt.Sprintf("%s:%d", host, port), clientTLS)
		must(t, err)
		defer conn.Close()
		must(t, conn.SetDeadline(time.Now().Add(3*time.Second)))

		_, err = conn.Write([]byte("secure\n"))
		must(t, err)

		// OnMessage 回包不带换行，按固定长度读取。
		want := []byte("TLS:secure")
		got := make([]byte, len(want))
		_, err = io.ReadFull(conn, got)
		must(t, err)
		if string(got) != string(want) {
			t.Fatalf("tls response = %q, want %q", string(got), string(want))
		}

		must(t, server.Close())
		waitServerExit(t, done)
	})
}
