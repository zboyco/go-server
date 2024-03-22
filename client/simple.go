package client

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/pkg/errors"
	goserver "github.com/zboyco/go-server"
)

type SimpleClient struct {
	network goserver.Network
	ip      string
	port    int
	conn    net.Conn // socket连接

	bufferSize       int
	maxScanTokenSize int
	scanner          *bufio.Scanner
	split            bufio.SplitFunc

	sync.Mutex
}

// NewSimpleClient 新建一个tcp客户端
func NewSimpleClient(network goserver.Network, ip string, port int) *SimpleClient {
	return &SimpleClient{
		network: network,
		ip:      ip,
		port:    port,
	}
}

// SetBufferSize 设置缓冲区大小
// 默认为 4 * 1024
func (client *SimpleClient) SetBufferSize(bufferSize int) {
	client.bufferSize = bufferSize
}

// SetMaxScanTokenSize 设置scan数据最大长度
// 默认为 64 * 1024
func (client *SimpleClient) SetMaxScanTokenSize(size int) {
	client.maxScanTokenSize = size
}

func (client *SimpleClient) SetScannerSplitFunc(split bufio.SplitFunc) {
	client.split = split
}

// Connect 连接
func (client *SimpleClient) Connect() error {
	client.Lock()
	defer client.Unlock()

	if client.conn != nil {
		return nil
	}

	serverAddr := fmt.Sprintf("%s:%d", client.ip, client.port)

	var conn net.Conn

	switch client.network {
	case goserver.TCP:
		tcpAddr, err := net.ResolveTCPAddr("tcp", serverAddr)
		if err != nil {
			return errors.Wrap(err, "ResolveTCPAddr error")
		}
		conn, err = net.DialTCP("tcp", nil, tcpAddr)
		if err != nil {
			return errors.Wrap(err, "DialTCP error")
		}
	case goserver.UDP:
		udpServerAddr, err := net.ResolveUDPAddr("udp", serverAddr)
		if err != nil {
			return errors.Wrap(err, "ResolveUDPAddr error")
		}
		conn, err = net.DialUDP("udp", nil, udpServerAddr)
		if err != nil {
			return errors.Wrap(err, "DialUDP error")
		}
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
	if client.split != nil {
		return client.receiveWithScanner()
	}
	return client.receiveRaw()
}

// receiveRaw 接收原始数据
func (client *SimpleClient) receiveRaw() ([]byte, error) {
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

// receiveWithScanner 接收scanner拆包数据
func (client *SimpleClient) receiveWithScanner() ([]byte, error) {
	func() {
		if client.scanner == nil {
			client.Lock()
			defer client.Unlock()

			if client.scanner != nil {
				return
			}

			// 创建scanner
			client.scanner = bufio.NewScanner(client.conn)
			if client.bufferSize > 0 || client.maxScanTokenSize > 0 {
				bufferSize := client.bufferSize
				maxScanTokenSize := client.maxScanTokenSize
				if bufferSize == 0 {
					bufferSize = 4 * 1024
				}
				if maxScanTokenSize == 0 {
					maxScanTokenSize = bufio.MaxScanTokenSize
				}
				if bufferSize > maxScanTokenSize {
					maxScanTokenSize = bufferSize
				}
				client.scanner.Buffer(make([]byte, 0, bufferSize), maxScanTokenSize)
			}

			// 设置分离函数
			client.scanner.Split(client.split)
		}
	}()

	// 获取数据
	if !client.scanner.Scan() {
		err := client.scanner.Err()
		if err != nil {
			return nil, err
		}
		return nil, io.EOF
	}

	return client.scanner.Bytes(), nil
}
