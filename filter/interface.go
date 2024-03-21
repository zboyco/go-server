package filter

import (
	"bufio"
	"net"
)

// ConnectionFilter 连接过滤器
type ConnectionFilter func(net.Conn) error

// ResolveActionFunc 解析数据返回actionName和message
type ResolveActionFunc func(token []byte) (actionName string, msg []byte, err error)

// ReceiveFilter 数据过滤器接口
type ReceiveFilter interface {
	SplitFunc() bufio.SplitFunc
	ResolveAction() ResolveActionFunc
}
