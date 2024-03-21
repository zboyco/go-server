package client

import (
	"fmt"
	"net"
	"sync"

	"github.com/pkg/errors"
)

type SimpleClient struct {
	ip         string
	port       int
	conn       net.Conn // socket连接
	bufferSize int

	sync.Mutex
}

// NewSimpleClient 新建一个tcp客户端
func NewSimpleClient(ip string, port int) *SimpleClient {
	return &SimpleClient{
		ip:   ip,
		port: port,
	}
}

// SetBufferSize 设置缓冲区大小
func (client *SimpleClient) SetBufferSize(bufferSize int) {
	client.bufferSize = bufferSize
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
	if client.conn == nil {
		return errors.New("conn is nil")
	}
	_, err := client.conn.Write(content)
	return err
}

func (client *SimpleClient) Receive() ([]byte, error) {
	var buf []byte
	if client.bufferSize > 0 {
		buf = make([]byte, client.bufferSize)
	} else {
		buf = make([]byte, 4*1024)
	}
	n, err := client.conn.Read(buf[:])
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
