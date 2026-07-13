package main

import (
	"bytes"
	"io"
	"log"
	"time"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/client"
)

// runTCPOnMessageDemo 演示默认换行拆包 + OnMessage 模式。
func runTCPOnMessageDemo() result {
	return runDemo("tcp on-message scanlines", func(t *demoT) {
		server := goserver.New(goserver.TCP, "127.0.0.1", 0)
		server.IdleSessionTimeOut = 0

		must(t, server.SetOnMessage(func(session *goserver.AppSession, token []byte) ([]byte, error) {
			// 默认 ScanLines 会去掉换行符，这里直接回显。
			log.Printf("[tcp-on-message] from %s: %s", session.ID, string(token))
			return append([]byte("ACK:"), token...), nil
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)

		cli := client.NewSimpleClient(goserver.TCP, host, port)
		must(t, cli.Connect())
		defer cli.Close()

		// 服务端回包不带换行，使用原始读取并设置超时，避免 ScanLines 阻塞。
		must(t, cli.GetRawConn().SetDeadline(time.Now().Add(3*time.Second)))
		must(t, cli.Send([]byte("hello-line\n")))

		want := []byte("ACK:hello-line")
		got := make([]byte, len(want))
		_, err := io.ReadFull(cli.GetRawConn(), got)
		must(t, err)
		if !bytes.Equal(got, want) {
			t.Fatalf("response = %q, want %q", string(got), string(want))
		}

		must(t, server.Stop())
		waitServerExit(t, done)
	})
}
