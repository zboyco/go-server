package server

import (
	"encoding/binary"
	"net"
	"time"
)

const (
	//HeaderLen 数据包头长度
	HeaderLen int = 2
)

//AppSession 客户端结构体
type AppSession struct {
	ID             int64     //连接唯一标识
	conn           net.Conn  //socket连接
	activeDateTime time.Time //最后活跃时间
	buffer         *buffer   //数据流
}

//Send 发送数据
func (session *AppSession) Send(buf []byte) {
	session.conn.Write(buf)
	//更新最后活跃时间
	session.activeDateTime = time.Now()
}

//Read 读取数据
//每次读取必然返回一个完整数据包或者错误信息
func (session *AppSession) Read() ([]byte, error) {
	var needRead bool
	for {
		if needRead {
			_, err := session.buffer.read()
			if err != nil {
				return nil, err
			}
		}
		headBuf, err := session.buffer.peek(HeaderLen)

		if err != nil {
			needRead = true
			continue
		}

		bodyLen := int(binary.BigEndian.Uint16(headBuf))

		bodyBuf, err := session.buffer.pick(HeaderLen, bodyLen)

		if err != nil {
			needRead = true
			continue
		}

		//更新最后活跃时间
		session.activeDateTime = time.Now()
		return bodyBuf, nil
	}
}
