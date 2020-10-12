package goserver_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"testing"
	"time"

	goserver "github.com/zboyco/go-server"
)

func init() {
	go func() {
		mainServer := goserver.New("", 9043)
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
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tcpAddr, _ := net.ResolveTCPAddr("tcp4", ":9043")
			conn, _ := net.DialTCP("tcp", nil, tcpAddr)

			defer conn.Close()

			var headSize int
			var headBytes = make([]byte, 4)
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
	//将bytes转为字符串
	result := string(bytes)

	//输出结果
	log.Println("接收到客户端[", client.ID, "]数据:", result)

	// client.Send([]byte("Got!"))
	return []byte("Got!"), nil
}

// 接收错误方法
func onError(err error) {
	//输出结果
	log.Println("错误: ", err)
}
