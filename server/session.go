package server

import (
	"log"
	"net"
	"time"
)

// AppSession 客户端结构体
type AppSession struct {
	ID             string    // 连接唯一标识
	conn           net.Conn  // socket连接
	activeDateTime time.Time // 最后活跃时间
	isClosed       bool      // 是否断开标记
}

// Send 发送数据
func (session *AppSession) Send(buf []byte) {
	session.conn.Write(buf)
	//更新最后活跃时间
	session.activeDateTime = time.Now()
}

// Close 关闭连接
func (session *AppSession) Close(reason string) {
	if !session.isClosed {
		session.isClosed = true
		log.Println("客户端[", session.ID, "]连接关闭，原因如下：", reason)
		if err := session.conn.Close(); err != nil {
			log.Println(err)
		}
	}
}
