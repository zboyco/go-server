package go_server_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/zboyco/go-server"
	"log"
	"net"
	"sync"
	"testing"
	"time"
)

func init() {
	go func() {
		mainServer := go_server.New("", 9043)
		mainServer.IdleSessionTimeOut = 10

		//根据协议定义分离规则
		mainServer.SetSplitFunc(func(data []byte, atEOF bool) (int, []byte, error) {
			if atEOF {
				return 0, nil, errors.New("EOF")
			}
			if data[0] != '$' || data[3] != '#' {
				return 0, nil, errors.New("数据异常")
			}
			if len(data) > 4 {
				length := uint16(0)
				binary.Read(bytes.NewReader(data[1:3]), binary.BigEndian, &length)
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
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tcpAddr, err := net.ResolveTCPAddr("tcp4", ":9043")
			if err != nil {
				t.Fatalf("Fatal error: %s", err.Error())
			}
			conn, err := net.DialTCP("tcp", nil, tcpAddr)
			if err != nil {
				t.Fatalf("Fatal error: %s", err.Error())
			}
			defer conn.Close()

			var headSize int
			var headBytes = make([]byte, 4)
			headBytes[0] = '$'
			headBytes[3] = '#'

			s := fmt.Sprintf("hello world - %v", i)
			content := []byte(s)
			headSize = len(content)
			binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
			conn.Write(headBytes)
			conn.Write(content)

			s = fmt.Sprintf("hello golang - %v", i)
			content = []byte(s)
			headSize = len(content)
			binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
			conn.Write(headBytes)
			conn.Write(content)

			s = fmt.Sprintf("hello socket - %v", i)
			content = []byte(s)
			headSize = len(content)
			binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
			conn.Write(headBytes)
			conn.Write(content)
		}(i)
	}
	wg.Wait()
	time.Sleep(3 * time.Second)
}

func BenchmarkSocket(b *testing.B) {
	for i := 0; i < b.N; i++ {
		tcpAddr, err := net.ResolveTCPAddr("tcp4", ":9043")
		if err != nil {
			b.Fatalf("Fatal error: %s", err.Error())
		}
		conn, err := net.DialTCP("tcp", nil, tcpAddr)
		if err != nil {
			b.Fatalf("Fatal error: %s", err.Error())
		}
		defer conn.Close()

		var headSize int
		var headBytes = make([]byte, 4)
		headBytes[0] = '$'
		headBytes[3] = '#'

		s := fmt.Sprintf("hello world - %v", i)
		content := []byte(s)
		headSize = len(content)
		binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
		conn.Write(headBytes)
		conn.Write(content)

		s = fmt.Sprintf("hello golang - %v", i)
		content = []byte(s)
		headSize = len(content)
		binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
		conn.Write(headBytes)
		conn.Write(content)

		s = fmt.Sprintf("hello socket - %v", i)
		content = []byte(s)
		headSize = len(content)
		binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
		conn.Write(headBytes)
		conn.Write(content)
	}
}

// 接收数据方法
func onMessage(client *go_server.AppSession, bytes []byte) {
	//将bytes转为字符串
	result := string(bytes)

	//输出结果
	log.Println("接收到客户端[", client.ID, "]数据:", result)

	// client.Send([]byte("Got!"))
}

// 接收错误方法
func onError(err error) {
	//输出结果
	log.Println("错误: ", err)
}
