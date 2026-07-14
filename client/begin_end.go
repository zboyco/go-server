package client

import (
	"context"
	"encoding/binary"
	"fmt"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/filter"
)

// BeginEndMarkClient 使用 BeginEndMarkReceiveFilter 的起止标记封装出站消息并解析入站帧。
type BeginEndMarkClient struct {
	*SimpleClient
	*filter.BeginEndMarkReceiveFilter
}

// NewBeginEndMarkClient 创建并校验一个起止标记协议客户端。
// receiveFilter 不能为 nil；其 SplitFunc 会作为最后一个客户端选项加入，因此覆盖 options 中较早的拆包配置。
func NewBeginEndMarkClient(network goserver.Network, address string, receiveFilter *filter.BeginEndMarkReceiveFilter, options ...Option) (*BeginEndMarkClient, error) {
	if receiveFilter == nil {
		return nil, fmt.Errorf("begin/end receive filter must not be nil")
	}
	options = append(options, WithScannerSplitFunc(receiveFilter.SplitFunc()))
	simpleClient, err := NewSimpleClient(network, address, options...)
	if err != nil {
		return nil, err
	}
	return &BeginEndMarkClient{SimpleClient: simpleClient, BeginEndMarkReceiveFilter: receiveFilter}, nil
}

// Send 在 content 前后追加配置的 Begin 和 End 标记，并写出完整协议包。
func (client *BeginEndMarkClient) Send(ctx context.Context, content []byte) error {
	packet := make([]byte, 0, len(client.Begin)+len(content)+len(client.End))
	packet = append(packet, client.Begin...)
	packet = append(packet, content...)
	packet = append(packet, client.End...)
	return client.SimpleClient.Send(ctx, packet)
}

// SendAction 把 actionPath 编码为 4 字节大端长度、路径和 content，再用起止标记封装并发送。
func (client *BeginEndMarkClient) SendAction(ctx context.Context, actionPath string, content []byte) error {
	packet := make([]byte, 4, 4+len(actionPath)+len(content))
	binary.BigEndian.PutUint32(packet, uint32(len(actionPath)))
	packet = append(packet, actionPath...)
	packet = append(packet, content...)
	return client.Send(ctx, packet)
}
