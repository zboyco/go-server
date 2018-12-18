package server

import "net"

//客户端结构体
type AppSession struct {
	conn net.Conn //socket连接
}

func (client *AppSession) Send(buf []byte) {
	client.conn.Write(buf)
}
