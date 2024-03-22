package filter

import (
	"bufio"
	"net"
)

type (
	// ConnectionFilterTCP TCP连接过滤器
	ConnectionFilterTCP func(net.Conn) error
	// ConnectionFilterUDP UDP连接过滤器
	ConnectionFilterUDP func(*net.UDPAddr) error
)

// ResolveActionFunc 解析数据返回actionName和message
type ResolveActionFunc func(token []byte) (actionName string, msg []byte, err error)

// ReceiveFilter 数据过滤器接口
type ReceiveFilter interface {
	SplitFunc() bufio.SplitFunc
	ResolveAction() ResolveActionFunc
}
