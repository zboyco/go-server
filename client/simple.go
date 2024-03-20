package client

import (
	"fmt"
	"net"
	"sync"

	"github.com/pkg/errors"
)

type SimpleClient struct {
	ip   string
	port int
	conn net.Conn // socket连接

	sync.Mutex
}

// NewSimpleClient 新建一个tcp客户端
func NewSimpleClient(ip string, port int) *SimpleClient {
	return &SimpleClient{
		ip:   ip,
		port: port,
	}
}

// Connect 连接
func (client *SimpleClient) Connect() error {
	client.Lock()
	defer client.Unlock()

	if client.conn != nil {
		return nil
	}

	tcpAddr, err := net.ResolveTCPAddr("tcp4", fmt.Sprintf("%s:%d", client.ip, client.port))
	if err != nil {
		return errors.Wrap(err, "ResolveTCPAddr error")
	}
	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	if err != nil {
		return errors.Wrap(err, "DialTCP error")
	}
	client.conn = conn
	return nil
}

// GetRawConn 获取原始连接
func (client *SimpleClient) GetRawConn() net.Conn {
	client.Lock()
	defer client.Unlock()

	return client.conn
}

// Close 关闭
func (client *SimpleClient) Close() error {
	client.Lock()
	defer client.Unlock()

	if client.conn == nil {
		return nil
	}
	defer func() {
		client.conn = nil
	}()
	return client.conn.Close()
}

// Send 发送
func (client *SimpleClient) Send(content []byte) error {
	client.Lock()
	defer client.Unlock()

	_, err := client.conn.Write(content)
	return err
}

func (client *SimpleClient) Receive() ([]byte, error) {
	client.Lock()
	defer client.Unlock()

	var buf [1024]byte
	n, err := client.conn.Read(buf[:])
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
