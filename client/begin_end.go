package client

import (
	"bytes"
	"encoding/binary"

	"github.com/pkg/errors"
	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/filter"
)

type BeginEndMarkClient struct {
	*SimpleClient
	*filter.BeginEndMarkReceiveFilter
}

// NewBeginEndMarkClient 新建一个开始结束标记的tcp客户端
func NewBeginEndMarkClient(network goserver.Network, ip string, port int, filter *filter.BeginEndMarkReceiveFilter) *BeginEndMarkClient {
	return &BeginEndMarkClient{
		SimpleClient:              NewSimpleClient(network, ip, port),
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
