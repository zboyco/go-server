package go_server

import (
	"log"
	"net"
)

// AppSession 客户端结构体
type AppSession struct {
	ID   string   // 连接唯一标识
	conn net.Conn // socket连接
}

// Send 发送数据
func (session *AppSession) Send(buf []byte) {
	if _, err := session.conn.Write(buf); err != nil {
		log.Println(err)
	}
}

// Close 关闭连接
func (session *AppSession) Close(reason string) {
	log.Println("客户端[", session.ID, "]连接关闭，原因如下：", reason)
	if err := session.conn.Close(); err != nil {
		log.Println(err)
	}
}
