package go_server

import (
	"errors"
	"log"
	"net"
)

// AppSession 客户端结构体
type AppSession struct {
	ID       string                 // 连接唯一标识
	IsClosed bool                   // 标记会话是否关闭
	conn     net.Conn               // socket连接
	attr     map[string]interface{} // 会话自定义属性
}

// Send 发送数据
func (session *AppSession) Send(buf []byte) {
	if session.IsClosed {
		log.Println("session is closed")
		return
	}
	if _, err := session.conn.Write(buf); err != nil {
		log.Println(err)
	}
}

// Close 关闭连接
func (session *AppSession) Close(reason string) {
	log.Println("客户端[", session.ID, "]连接关闭，原因如下：", reason)
	session.IsClosed = true
	if err := session.conn.Close(); err != nil {
		log.Println(err)
	}
}

// AddAttr 添加会话属性
func (session *AppSession) AddAttr(key string, value interface{}) error {
	if _, exist := session.attr[key]; exist {
		return errors.New("attribute already exist")
	}
	session.attr[key] = value
	return nil
}

// SetAttr 设置会话属性
func (session *AppSession) SetAttr(key string, value interface{}) {
	session.attr[key] = value
}

// GetAttr 获取会话属性
func (session *AppSession) GetAttr(key string) (interface{}, error) {
	if _, exist := session.attr[key]; exist {
		return session.attr[key], nil
	}
	return nil, errors.New("attribute not exist")
}

// DelAttr 删除会话属性
func (session *AppSession) DelAttr(key string) error {
	if _, exist := session.attr[key]; !exist {
		return errors.New("attribute not exist")
	}
	delete(session.attr, key)
	return nil
}
