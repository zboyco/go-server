package goserver_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"testing"
	"time"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/client"
	"github.com/zboyco/go-server/filter"
)

type module struct{}

func (m *module) Root() string {
	return "/"
}

func (m *module) Say(client *goserver.AppSession, token []byte) ([]byte, error) {
	// 将bytes转为字符串
	result := string(token)

	// 输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)

	return token, nil
}

func BeginEndServer(network goserver.Network) {
	go func() {
		mainServer := goserver.New(network, "", 8080)
		mainServer.SetEOF([]byte("x$$io.EOF$$x"))

		mainServer.IdleSessionTimeOut = 5

		mainServer.SetReceiveFilter(&filter.BeginEndMarkReceiveFilter{
			Begin: []byte{'!', '$'},
			End:   []byte{'$', '!'},
		})

		if err := mainServer.RegisterModule(&module{}); err != nil {
			log.Panic(err)
		}

		mainServer.RegisterSendPacketFilter(goserver.Middlewares{
			func(as *goserver.AppSession, b []byte) ([]byte, error) {
				return bytes.Join([][]byte{{'!', '$'}, b, {'$', '!'}}, nil), nil
			},
		})

		mainServer.SetOnMessage(onMessage)

		mainServer.SetOnError(onError)

		mainServer.SetOnSessionClosed(func(session *goserver.AppSession, reason string) {
			log.Println("会话关闭:", session.ID, "原因", reason)
		})

		mainServer.Start()
	}()
}

func StartServer() {
	go func() {
		mainServer := goserver.NewTCP("", 8080)
		mainServer.IdleSessionTimeOut = 10

		// 根据协议定义分离规则
		mainServer.SetSplitFunc(func(data []byte, atEOF bool) (int, []byte, error) {
			if atEOF {
				return 0, nil, errors.New("EOF")
			}
			if data[0] != '$' || data[3] != '#' {
				return 0, nil, errors.New("数据异常")
			}
			if len(data) > 4 {
				length := uint16(0)
				_ = binary.Read(bytes.NewReader(data[1:3]), binary.BigEndian, &length)
				if int(length)+4 <= len(data) {
					return int(length) + 4, data[4 : int(length)+4], nil
				}
			}
			return 0, nil, nil
		})

		mainServer.SetOnMessage(onMessage)

		mainServer.SetOnError(onError)

		mainServer.Start()
	}()
}

func TestSocket(t *testing.T) {
	t.Run("default socket", func(t *testing.T) {
		StartServer()

		var wg sync.WaitGroup

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				tcpAddr, _ := net.ResolveTCPAddr("tcp4", ":8080")
				conn, _ := net.DialTCP("tcp", nil, tcpAddr)

				defer conn.Close()

				var headSize int
				headBytes := make([]byte, 4)
				headBytes[0] = '$'
				headBytes[3] = '#'

				s := fmt.Sprintf("hello world - %v", i)
				content := []byte(s)
				headSize = len(content)
				binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
				_, _ = conn.Write(headBytes)
				_, _ = conn.Write(content)

				s = fmt.Sprintf("hello golang - %v", i)
				content = []byte(s)
				headSize = len(content)
				binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
				_, _ = conn.Write(headBytes)
				_, _ = conn.Write(content)

				s = fmt.Sprintf("hello socket - %v", i)
				content = []byte(s)
				headSize = len(content)
				binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
				_, _ = conn.Write(headBytes)
				_, _ = conn.Write(content)
			}(i)
		}
		wg.Wait()
		time.Sleep(3 * time.Second)
	})

	t.Run("begin-end socket", func(t *testing.T) {
		BeginEndServer(goserver.TCP)
		time.Sleep(3 * time.Second)
		filter := &filter.BeginEndMarkReceiveFilter{
			Begin: []byte{'!', '$'},
			End:   []byte{'$', '!'},
		}
		c := client.NewBeginEndMarkClient(goserver.TCP, "", 8080, filter)
		// c.SetScannerSplitFunc(c.SplitFunc())

		if err := c.Connect(); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				_ = c.SendAction("/say", []byte("hello world"))
				time.Sleep(1 * time.Second)
			}
		}()
		wg.Add(1)
		go func(t *testing.T) {
			defer wg.Done()
			for {
				result, err := c.Receive()
				if err != nil {
					t.Error(err)
					break
				}
				log.Println("接收到服务器数据:", string(result))
			}
		}(t)
		wg.Wait()
	})

	t.Run("begin-end udp socket", func(t *testing.T) {
		BeginEndServer(goserver.UDP)
		time.Sleep(3 * time.Second)
		filter := &filter.BeginEndMarkReceiveFilter{
			Begin: []byte{'!', '$'},
			End:   []byte{'$', '!'},
		}
		c := client.NewBeginEndMarkClient(goserver.UDP, "", 8080, filter)
		c.SetScannerSplitFunc(c.SplitFunc())

		if err := c.Connect(); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				_ = c.SendAction("/say", []byte("hello world"))
				time.Sleep(1 * time.Second)
			}
		}()
		wg.Add(1)
		go func(t *testing.T) {
			defer wg.Done()
			for {
				result, err := c.Receive()
				if err != nil {
					t.Error(err)
					break
				}

				if string(result) == "x$$io.EOF$$x" {
					t.Error(io.EOF)
					break
				}

				log.Println("接收到服务器数据:", string(result))
			}
		}(t)
		wg.Wait()
	})
}

func BenchmarkSocket(b *testing.B) {
	StartServer()

	for i := 0; i < b.N; i++ {
		tcpAddr, err := net.ResolveTCPAddr("tcp4", ":8080")
		if err != nil {
			b.Fatalf("Fatal error: %s", err.Error())
		}
		conn, err := net.DialTCP("tcp", nil, tcpAddr)
		if err != nil {
			b.Fatalf("Fatal error: %s", err.Error())
		}
		defer conn.Close()

		var headSize int
		headBytes := make([]byte, 4)
		headBytes[0] = '$'
		headBytes[3] = '#'

		s := fmt.Sprintf("hello world - %v", i)
		content := []byte(s)
		headSize = len(content)
		binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
		_, _ = conn.Write(headBytes)
		_, _ = conn.Write(content)

		s = fmt.Sprintf("hello golang - %v", i)
		content = []byte(s)
		headSize = len(content)
		binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
		_, _ = conn.Write(headBytes)
		_, _ = conn.Write(content)

		s = fmt.Sprintf("hello socket - %v", i)
		content = []byte(s)
		headSize = len(content)
		binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
		_, _ = conn.Write(headBytes)
		_, _ = conn.Write(content)
	}
}

// 接收数据方法
func onMessage(client *goserver.AppSession, bytes []byte) ([]byte, error) {
	// 将bytes转为字符串
	result := string(bytes)

	// 输出结果
	log.Println("接收到客户端[", client.ID, "]数据:", result)

	// client.Send([]byte("Got!"))
	return []byte("Got!"), nil
}

// 接收错误方法
func onError(err error) {
	// 输出结果
	log.Println("错误: ", err)
}
