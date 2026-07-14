package main

import (
	"encoding/binary"
	"errors"
	"time"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/filter"
)

// runProtocolErrorDemo 演示内置过滤器错误及服务端 OnError 的 errors.Is 判断方式。
func runProtocolErrorDemo() result {
	return runDemo("protocol error handling", func(t *demoT) {
		assertBuiltInProtocolErrors(t)

		errCh := make(chan error, 1)
		server := newTCPServer(t, "127.0.0.1", 0, goserver.WithIdleTimeout(0))
		must(t, server.SetReceiveFilter(&filter.FixedHeaderReceiveFilter{}))
		must(t, server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, _ []byte) ([]byte, error) {
			return nil, nil
		}))
		must(t, server.SetOnError(func(err error) {
			select {
			case errCh <- err:
			default:
			}
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)
		cli := newSimpleClient(t, goserver.TCP, host, port)
		must(t, cli.Connect(backgroundContext))
		defer cli.Close()

		// 包长小于固定头的 8 字节时，服务端应通过 OnError 返回可识别的协议错误。
		invalidPacket := make([]byte, 4)
		binary.BigEndian.PutUint32(invalidPacket, 4)
		must(t, cli.Send(backgroundContext, invalidPacket))

		select {
		case err := <-errCh:
			if !errors.Is(err, filter.ErrInvalidPackageLength) {
				t.Fatalf("OnError = %v, want errors.Is ErrInvalidPackageLength", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for protocol error")
		}

		must(t, server.Close())
		waitServerExit(t, done)
	})
}

// assertBuiltInProtocolErrors 验证四个导出协议错误均可通过 errors.Is 判断。
func assertBuiltInProtocolErrors(t *demoT) {
	t.Helper()

	_, _, err := (&filter.BeginEndMarkReceiveFilter{}).SplitFunc()([]byte("x"), false)
	if !errors.Is(err, filter.ErrInvalidMarkLength) {
		t.Fatalf("empty mark error = %v, want %v", err, filter.ErrInvalidMarkLength)
	}

	_, _, err = (&filter.BeginEndMarkReceiveFilter{}).ResolveAction()([]byte{1, 2, 3})
	if !errors.Is(err, filter.ErrInvalidTokenLength) {
		t.Fatalf("short token error = %v, want %v", err, filter.ErrInvalidTokenLength)
	}

	token := make([]byte, 8)
	binary.BigEndian.PutUint32(token[4:8], 1)
	_, _, err = (&filter.FixedHeaderReceiveFilter{}).ResolveAction()(token)
	if !errors.Is(err, filter.ErrInvalidActionNameLength) {
		t.Fatalf("action length error = %v, want %v", err, filter.ErrInvalidActionNameLength)
	}

	packet := make([]byte, 4)
	binary.BigEndian.PutUint32(packet, 4)
	_, _, err = (&filter.FixedHeaderReceiveFilter{}).SplitFunc()(packet, false)
	if !errors.Is(err, filter.ErrInvalidPackageLength) {
		t.Fatalf("package length error = %v, want %v", err, filter.ErrInvalidPackageLength)
	}
}
