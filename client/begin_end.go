package client

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"

	"github.com/pkg/errors"
	goserver "github.com/zboyco/go-server"
)

type BeginEndMarkClient struct {
	SimpleClient
	*goserver.BeginEndMarkReceiveFilter

	scanner *bufio.Scanner
}

// NewBeginEndMarkClient 新建一个开始结束标记的tcp客户端
func NewBeginEndMarkClient(ip string, port int, filter *goserver.BeginEndMarkReceiveFilter) *BeginEndMarkClient {
	return &BeginEndMarkClient{
		SimpleClient:              SimpleClient{ip: ip, port: port},
		BeginEndMarkReceiveFilter: filter,
	}
}

// Connect 连接
func (client *BeginEndMarkClient) Connect() error {
	if client.BeginEndMarkReceiveFilter == nil {
		return errors.New("BeginEndMarkReceiveFilter is nil")
	}
	return client.SimpleClient.Connect()
}

// Send 发送
func (client *BeginEndMarkClient) Send(content []byte) error {
	return client.SimpleClient.Send(bytes.Join([][]byte{client.Begin, content, client.End}, nil))
}

// SendAction 发送action
func (client *BeginEndMarkClient) SendAction(actionPath string, content []byte) error {
	headBytes := make([]byte, 4)

	actionPathLength := len(actionPath)

	binary.BigEndian.PutUint32(headBytes, uint32(actionPathLength))

	return client.Send(bytes.Join([][]byte{headBytes, []byte(actionPath), content}, nil))
}

// Receive 接收
func (client *BeginEndMarkClient) Receive() ([]byte, error) {
	client.Lock()
	defer client.Unlock()

	if client.scanner == nil {
		// 创建scanner
		client.scanner = bufio.NewScanner(client.conn)
		client.scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 分配64KB的缓冲区，并设置最大令牌大小为1MB

		// 设置分离函数
		client.scanner.Split(client.BeginEndMarkReceiveFilter.SplitFunc())
	}

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
