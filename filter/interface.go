package filter

import (
	"bufio"
	"net"
)

type (
	// ConnectionFilterTCP 校验一条已完成 TLS 握手、但尚未创建 AppSession 的 TCP 连接。
	// 返回非 nil 错误会拒绝并关闭该连接。
	ConnectionFilterTCP func(net.Conn) error
	// ConnectionFilterUDP 校验一个首次出现、但尚未创建 AppSession 的 UDP 远端地址。
	// 返回非 nil 错误会丢弃当前数据报且不注册 endpoint 会话。
	ConnectionFilterUDP func(*net.UDPAddr) error
)

// ResolveActionFunc 从一个完整帧中解析 action 路径和业务负载。
// 返回的 msg 可以引用 token 原始内存，仅保证在当前同步处理链返回前有效。
type ResolveActionFunc func(token []byte) (actionName string, msg []byte, err error)

// ReceiveFilter 为 TCP 字节流和 UDP 数据报同时提供拆包与 action 路径解析能力。
// 两个方法都必须返回非 nil 函数；Server 会在 SetReceiveFilter 时立即校验。
type ReceiveFilter interface {
	// SplitFunc 返回负责识别完整应用层帧的 bufio.SplitFunc。
	SplitFunc() bufio.SplitFunc
	// ResolveAction 返回负责从完整帧中解析路由路径和业务负载的函数。
	ResolveAction() ResolveActionFunc
}
