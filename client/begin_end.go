package client

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"

	"github.com/pkg/errors"
	"github.com/zboyco/go-server/filter"
)

type BeginEndMarkClient struct {
	SimpleClient
	*filter.BeginEndMarkReceiveFilter

	scanner          *bufio.Scanner
	maxScanTokenSize int
}

// NewBeginEndMarkClient 新建一个开始结束标记的tcp客户端
func NewBeginEndMarkClient(ip string, port int, filter *filter.BeginEndMarkReceiveFilter) *BeginEndMarkClient {
	return &BeginEndMarkClient{
		SimpleClient:              SimpleClient{ip: ip, port: port},
		BeginEndMarkReceiveFilter: filter,
	}
}

// SetMaxScanTokenSize 设置数据最大长度
func (client *BeginEndMarkClient) SetMaxScanTokenSize(size int) {
	client.maxScanTokenSize = size
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
	client.RLock()
	defer client.RUnlock()

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
			client.scanner.Split(client.BeginEndMarkReceiveFilter.SplitFunc())
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
