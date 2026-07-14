// Package filter 定义服务端连接准入、消息拆包和 action 路径解析接口。
//
// 包内提供起止标记及固定长度头两种 ReceiveFilter 实现，也允许调用方通过
// bufio.SplitFunc 和 ResolveActionFunc 组合自定义应用层协议。
package filter
