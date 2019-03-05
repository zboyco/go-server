package server_test

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/zboyco/go-server/server"
)

func init() {
	go func() {
		mainServer := server.New("127.0.0.1", 9043, 10, 6)

		mainServer.OnMessage = onMessage

		mainServer.OnError = onError

		mainServer.Start()
	}()
}

func TestSocket(t *testing.T) {
	tcpAddr, err := net.ResolveTCPAddr("tcp4", "127.0.0.1:9043")
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
	s := "hello world"
	content := []byte(s)
	headSize = len(content)
	binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
	conn.Write(headBytes)
	conn.Write(content)

	s = "hello go"
	content = []byte(s)
	headSize = len(content)
	binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
	conn.Write(headBytes)
	conn.Write(content)

	s = "hello tcp"
	content = []byte(s)
	headSize = len(content)
	binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
	conn.Write(headBytes)
	conn.Write(content)
	time.Sleep(time.Second * 3)
}

func BenchmarkSocket(b *testing.B) {
	for i := 0; i < b.N; i++ {
		tcpAddr, err := net.ResolveTCPAddr("tcp4", "127.0.0.1:9043")
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
		s := "hello world"
		content := []byte(s)
		headSize = len(content)
		binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
		conn.Write(headBytes)
		conn.Write(content)

		s = "hello go"
		content = []byte(s)
		headSize = len(content)
		binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
		conn.Write(headBytes)
		conn.Write(content)

		s = "hello tcp"
		content = []byte(s)
		headSize = len(content)
		binary.BigEndian.PutUint16(headBytes[1:], uint16(headSize))
		conn.Write(headBytes)
		conn.Write(content)
	}
}

// 接收数据方法
func onMessage(client *server.AppSession, bytes []byte) {
	//将bytes转为字符串
	//result := string(bytes)

	//输出结果
	//log.Println("接收到客户[", client.ID, "]数据:", result)

	// client.Send([]byte("Got!"))
}

// 接收错误方法
func onError(err error) {
	//输出结果
	//log.Println("错误: ", err)
}
