package server

import (
	"encoding/binary"
	"errors"
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
	//判断是否需要读取数据
	needRead := session.buffer.len() < HeaderLen
	for {
		if needRead {
			_, err := session.buffer.read()
			if err != nil {
				return nil, err
			}
		}
		//查看前4个数据包头数据
		headBuf, err := session.buffer.peek(HeaderLen)

		if err != nil {
			needRead = true
			continue
		}

		//判断1和4位是否为指定的起始码和结束码
		if headBuf[0] != HeaderStartByte || headBuf[3] != HeaderEndByte {
			return nil, errors.New("接收到异常数据")
		}

		//计算数据包内容长度
		bodyLen := int(binary.BigEndian.Uint16(headBuf[1:3]))

		//提取数据内容
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
