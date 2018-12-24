package server

import (
	"fmt"
	"net"
	"time"
)

const (
	//HeaderLen 数据包头长度
	HeaderLen int = 4
	//HeaderStartByte 数据包头部起始码
	HeaderStartByte byte = '$'
	//HeaderEndByte 数据包头部结束码
	HeaderEndByte byte = '#'
)

// AppSession 客户端结构体
type AppSession struct {
	ID             int64     //连接唯一标识
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
	fmt.Println("客户端[", session.ID, "]连接已关闭!")
}
