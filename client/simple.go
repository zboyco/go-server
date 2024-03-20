package client

import (
	"fmt"
	"net"

	"github.com/pkg/errors"
)

type SimpleClient struct {
	ip   string
	port int
	conn net.Conn // socket连接
}

func NewSimpleClient(ip string, port int) *SimpleClient {
	return &SimpleClient{
		ip:   ip,
		port: port,
	}
}

func (client *SimpleClient) Connect() error {
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

func (client *SimpleClient) GetConn() net.Conn {
	return client.conn
}

func (client *SimpleClient) Close() error {
	return client.conn.Close()
}

func (client *SimpleClient) Send(content []byte) error {
	_, err := client.conn.Write(content)
	return err
}

func (client *SimpleClient) Receive() ([]byte, error) {
	var buf [1024]byte
	n, err := client.conn.Read(buf[:])
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
