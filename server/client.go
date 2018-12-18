package server

import (
	"net"
	"time"
)

//客户端结构体
type AppSession struct {
	ID             int64     //连接唯一标识
	conn           net.Conn  //socket连接
	activeDateTime time.Time //最后活跃时间
}

//发送数据
func (session *AppSession) Send(buf []byte) {
	session.conn.Write(buf)
	//更新最后活跃时间
	session.activeDateTime = time.Now()
}

//读取数据
func (session *AppSession) Read() ([]byte, error) {

	//定义一个数据接收Buffer
	var buf [10240]byte

	//读取数据,io.Reader 需要传入一个byte切片
	n, err := session.conn.Read(buf[0:])

	if err != nil {
		return nil, err
	}

	//更新最后活跃时间
	session.activeDateTime = time.Now()
	return buf[0:n], nil
}
