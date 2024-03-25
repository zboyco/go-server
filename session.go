package goserver

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"
)

// AppSession 客户端结构体
type AppSession struct {
	ID               string                 // 连接唯一标识
	IsClosed         bool                   // 标记会话是否关闭
	attr             map[string]interface{} // 会话自定义属性
	sendPacketFilter Middlewares            // 发送数据过滤

	network Network  // 传输协议
	conn    net.Conn // socket连接

	udpAddr         *net.UDPAddr  // udp地址
	udpClientIO     io.ReadWriter // 用于udp客户端
	udpReadDeadline time.Time     // 超时时间,用于udp超时检测
}

// SendRaw 发送原始数据
func (session *AppSession) SendRaw(buf []byte) error {
	if session.IsClosed {
		return errors.New("session is closed")
	}

	switch session.network {
	case TCP:
		if _, err := session.conn.Write(buf); err != nil {
			return err
		}
	case UDP:
		if _, err := session.conn.(*net.UDPConn).WriteToUDP(buf, session.udpAddr); err != nil {
			return err
		}
	}
	return nil
}

// Send 发送打包后的数据
func (session *AppSession) Send(buf []byte) error {
	var err error
	for _, fn := range session.sendPacketFilter {
		buf, err = fn(session, buf)
		if err != nil {
			return err
		}
	}

	return session.SendRaw(buf)
}

// Close 关闭连接
func (session *AppSession) Close(reason string) {
	slog.Debug(fmt.Sprintf("client[%s] close because %s", session.ID, reason))
	session.IsClosed = true
	if session.network == UDP {
		return
	}
	if err := session.conn.Close(); err != nil {
		slog.Error(fmt.Sprintf("client[%s] close error: %s", session.ID, err.Error()))
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
