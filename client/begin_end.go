package client

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"sync"

	"github.com/pkg/errors"
	goserver "github.com/zboyco/go-server"
)

type BeginEndMarkClient struct {
	SimpleClient
	*goserver.BeginEndMarkReceiveFilter

	scanner *bufio.Scanner
	sync.Mutex
}

func NewBeginEndMarkClient(ip string, port int, filter *goserver.BeginEndMarkReceiveFilter) *BeginEndMarkClient {
	return &BeginEndMarkClient{
		SimpleClient:              SimpleClient{ip: ip, port: port},
		BeginEndMarkReceiveFilter: filter,
	}
}

func (client *BeginEndMarkClient) Connect() error {
	if client.BeginEndMarkReceiveFilter == nil {
		return errors.New("BeginEndMarkReceiveFilter is nil")
	}
	return client.SimpleClient.Connect()
}

func (client *BeginEndMarkClient) Send(content []byte) error {
	_, err := client.conn.Write(client.Begin)
	if err != nil {
		return err
	}

	_, err = client.conn.Write(content)
	if err != nil {
		return err
	}

	_, err = client.conn.Write(client.End)
	if err != nil {
		return err
	}
	return nil
}

func (client *BeginEndMarkClient) SendAction(actionPath string, content []byte) error {
	headBytes := make([]byte, 4)

	actionPathLength := len(actionPath)

	binary.BigEndian.PutUint32(headBytes, uint32(actionPathLength))

	return client.Send(bytes.Join([][]byte{headBytes, []byte(actionPath), content}, nil))
}

func (client *BeginEndMarkClient) Receive() ([]byte, error) {
	client.Lock()
	defer client.Unlock()

	if client.scanner == nil {
		// 创建scanner
		client.scanner = bufio.NewScanner(client.conn)

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
