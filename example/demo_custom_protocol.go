package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/client"
	"github.com/zboyco/go-server/filter"
)

// runCustomSplitDemo 演示 SetSplitFunc、自定义长度前缀协议和客户端 Scanner 配置。
func runCustomSplitDemo() result {
	return runDemo("custom split function", func(t *demoT) {
		server := goserver.NewTCP("127.0.0.1", 0)
		server.IdleSessionTimeOut = 0
		must(t, server.SetSplitFunc(lengthPrefixSplit))
		must(t, server.RegisterSendPacketFilter(goserver.Middlewares{
			func(_ *goserver.AppSession, buf []byte) ([]byte, error) {
				return lengthPrefixPacket(buf)
			},
		}))
		must(t, server.SetOnMessage(func(_ *goserver.AppSession, token []byte) ([]byte, error) {
			return append([]byte("CUSTOM:"), token...), nil
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)

		cli := client.NewSimpleClient(goserver.TCP, host, port)
		cli.SetBufferSize(16)
		cli.SetMaxScanTokenSize(1024)
		cli.SetScannerSplitFunc(lengthPrefixSplit)
		must(t, cli.Connect())
		defer cli.Close()
		must(t, cli.GetRawConn().SetDeadline(time.Now().Add(3*time.Second)))

		packet, err := lengthPrefixPacket([]byte("hello"))
		must(t, err)
		must(t, cli.Send(packet))
		got, err := cli.Receive()
		must(t, err)
		if string(got) != "CUSTOM:hello" {
			t.Fatalf("custom split response = %q, want %q", string(got), "CUSTOM:hello")
		}

		must(t, server.Stop())
		waitServerExit(t, done)
	})
}

// lengthPrefixSplit 按两字节大端长度头拆分数据包，并返回不含长度头的正文。
func lengthPrefixSplit(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if len(data) < 2 {
		if atEOF && len(data) != 0 {
			return 0, nil, io.ErrUnexpectedEOF
		}
		return 0, nil, nil
	}
	length := int(binary.BigEndian.Uint16(data[:2]))
	packetLength := 2 + length
	if len(data) < packetLength {
		if atEOF {
			return 0, nil, io.ErrUnexpectedEOF
		}
		return 0, nil, nil
	}
	return packetLength, data[2:packetLength], nil
}

// lengthPrefixPacket 为正文添加两字节大端长度头，正文过长时返回错误。
func lengthPrefixPacket(body []byte) ([]byte, error) {
	const maxUint16 = 1<<16 - 1
	if len(body) > maxUint16 {
		return nil, fmt.Errorf("length-prefix body is too large: %d", len(body))
	}
	packet := make([]byte, 2+len(body))
	binary.BigEndian.PutUint16(packet[:2], uint16(len(body)))
	copy(packet[2:], body)
	return packet, nil
}

// pipeReceiveFilter 使用“路由|正文\n”格式演示自定义 ReceiveFilter。
type pipeReceiveFilter struct{}

// SplitFunc 使用标准库 ScanLines 完成自定义协议的帧拆分。
func (*pipeReceiveFilter) SplitFunc() bufio.SplitFunc {
	return bufio.ScanLines
}

// ResolveAction 将竖线前后的内容解析为路由名称和消息正文。
func (*pipeReceiveFilter) ResolveAction() filter.ResolveActionFunc {
	return func(token []byte) (string, []byte, error) {
		separator := bytes.IndexByte(token, '|')
		if separator <= 0 {
			return "", nil, fmt.Errorf("custom protocol: missing action separator")
		}
		return string(token[:separator]), token[separator+1:], nil
	}
}

// runCustomReceiveFilterDemo 演示自定义 ReceiveFilter 与命令路由协作。
func runCustomReceiveFilterDemo() result {
	return runDemo("custom receive filter", func(t *demoT) {
		server := goserver.NewTCP("127.0.0.1", 0)
		server.IdleSessionTimeOut = 0
		must(t, server.SetReceiveFilter(&pipeReceiveFilter{}))
		must(t, server.Action("/upper", func(_ *goserver.AppSession, token []byte) ([]byte, error) {
			return bytes.ToUpper(token), nil
		}))

		addr, done := startServer(t, server)
		host, port := hostPort(t, addr)

		cli := client.NewSimpleClient(goserver.TCP, host, port)
		must(t, cli.Connect())
		defer cli.Close()
		must(t, cli.GetRawConn().SetDeadline(time.Now().Add(3*time.Second)))
		must(t, cli.Send([]byte("/upper|custom-filter\n")))

		want := []byte("CUSTOM-FILTER")
		got := make([]byte, len(want))
		_, err := io.ReadFull(cli.GetRawConn(), got)
		must(t, err)
		if !bytes.Equal(got, want) {
			t.Fatalf("custom filter response = %q, want %q", string(got), string(want))
		}

		must(t, server.Stop())
		waitServerExit(t, done)
	})
}
