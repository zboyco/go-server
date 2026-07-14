// Package client 提供与 goserver 协议模型配套的轻量 TCP/UDP 客户端。
//
// SimpleClient 支持 context、TLS、读写超时和 bufio.SplitFunc 拆包；
// BeginEndMarkClient 额外提供起止标记协议及 action 路径编码。未建立连接时，
// Send 和 Receive 返回 ErrNotConnected。
package client
