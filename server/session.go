package server

import (
	"log"
	"net"
	"time"
)

// AppSession 客户端结构体
type AppSession struct {
	ID             string    //连接唯一标识
	conn           net.Conn  //socket连接
	activeDateTime time.Time //最后活跃时间
}

// Send 发送数据
func (session *AppSession) Send(buf []byte) {
	session.conn.Write(buf)
	//更新最后活跃时间
	session.activeDateTime = time.Now()
}

// Close 关闭连接
func (session *AppSession) Close(reason string) {
	session.conn.Close()
	log.Println("客户端[", session.ID, "]连接已关闭，原因：", reason)
}
