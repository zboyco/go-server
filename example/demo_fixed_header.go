package main

import (
	"bytes"
	"io"
	"log"
	"time"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/client"
	"github.com/zboyco/go-server/filter"
)

// runFixedHeaderDemo 演示 FixedHeader 协议拆包与路由。
func runFixedHeaderDemo() result {
	return runDemo("tcp fixed-header protocol", func(t *demoT) {
		server := goserver.NewTCP("127.0.0.1", 0)
		server.IdleSessionTimeOut = 0

		recvFilter := &filter.FixedHeaderReceiveFilter{}
		must(t, server.SetReceiveFilter(recvFilter))
		must(t, server.Action("/add", func(_ *goserver.AppSession, token []byte) ([]byte, error) {
			return append([]byte("SUM:"), token...), nil
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)

		cli := client.NewSimpleClient(goserver.TCP, host, port)
		must(t, cli.Connect())
		defer cli.Close()
		must(t, cli.GetRawConn().SetDeadline(time.Now().Add(3*time.Second)))

		packet := fixedHeaderPacket("/add", []byte("1+2"))
		must(t, cli.Send(packet))

		// 服务端返回的是 action 结果原始字节，不带协议头。
		want := []byte("SUM:1+2")
		got := make([]byte, len(want))
		_, err := io.ReadFull(cli.GetRawConn(), got)
		must(t, err)
		if !bytes.Equal(got, want) {
			t.Fatalf("fixed-header response = %q, want %q", string(got), string(want))
		}
		log.Printf("[fixed-header] response=%s", string(got))

		must(t, server.Stop())
		waitServerExit(t, done)
	})
}
