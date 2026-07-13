package goserver

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// AppSession 客户端结构体
type AppSession struct {
	ID               string      // 连接唯一标识
	IsClosed         bool        // 标记会话是否关闭
	closed           uint32      // 内部关闭标记，用于并发安全地判断会话状态
	closeOnce        sync.Once   // 保证关闭流程只执行一次
	attr             sessionAttr // 会话自定义属性
	sendPacketFilter Middlewares // 发送数据过滤

	network Network  // 传输协议
	conn    net.Conn // socket连接

	udpAddr               *net.UDPAddr  // udp地址
	udpClientIO           io.ReadWriter // 用于udp客户端
	udpReadDeadline       time.Time     // 超时时间,用于udp超时检测
	udpReadDeadlineLocker sync.RWMutex  // 保护 udpReadDeadline 的并发读写

	closeTrigger func(reason string) // 会话关闭触发器
}

// sessionAttr 用读写锁保护会话属性，避免 action、回调和遍历时并发读写 map。
type sessionAttr struct {
	sync.RWMutex
	values map[string]interface{}
}

// ensureValues 在持有写锁时初始化属性 map。
func (attr *sessionAttr) ensureValues() {
	if attr.values == nil {
		attr.values = make(map[string]interface{})
	}
}

// isClosed 返回当前会话是否已经进入关闭状态。
func (session *AppSession) isClosed() bool {
	return atomic.LoadUint32(&session.closed) == 1
}

// closeWithEOF 关闭连接，并在关闭前尽力发送 EOF 标记。
func (session *AppSession) closeWithEOF(reason string, eof []byte) {
	session.closeOnce.Do(func() {
		slog.Debug(fmt.Sprintf("client[%s] close because %s", session.ID, reason))

		if len(eof) != 0 {
			_ = session.Send(eof)
		}

		atomic.StoreUint32(&session.closed, 1)
		session.IsClosed = true

		if closer, ok := session.udpClientIO.(io.Closer); ok {
			_ = closer.Close()
		}

		if session.network == TCP && session.conn != nil {
			if err := session.conn.Close(); err != nil {
				slog.Error(fmt.Sprintf("client[%s] close error: %s", session.ID, err.Error()))
			}
		}

		if session.closeTrigger != nil {
			session.closeTrigger(reason)
		}
	})
}

// SendRaw 发送原始数据
func (session *AppSession) SendRaw(buf []byte) error {
	if session.isClosed() {
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
	session.closeWithEOF(reason, nil)
}

// AddAttr 添加会话属性
func (session *AppSession) AddAttr(key string, value interface{}) error {
	session.attr.Lock()
	defer session.attr.Unlock()

	session.attr.ensureValues()
	if _, exist := session.attr.values[key]; exist {
		return errors.New("attribute already exist")
	}
	session.attr.values[key] = value
	return nil
}

// SetAttr 设置会话属性
func (session *AppSession) SetAttr(key string, value interface{}) {
	session.attr.Lock()
	defer session.attr.Unlock()

	session.attr.ensureValues()
	session.attr.values[key] = value
}

// GetAttr 获取会话属性
func (session *AppSession) GetAttr(key string) (interface{}, error) {
	session.attr.RLock()
	defer session.attr.RUnlock()

	if value, exist := session.attr.values[key]; exist {
		return value, nil
	}
	return nil, errors.New("attribute not exist")
}

// DelAttr 删除会话属性
func (session *AppSession) DelAttr(key string) error {
	session.attr.Lock()
	defer session.attr.Unlock()

	if _, exist := session.attr.values[key]; !exist {
		return errors.New("attribute not exist")
	}
	delete(session.attr.values, key)
	return nil
}

// attrSnapshot 返回当前会话属性的浅拷贝，避免调用方持有内部 map。
func (session *AppSession) attrSnapshot() map[string]interface{} {
	session.attr.RLock()
	defer session.attr.RUnlock()

	result := make(map[string]interface{}, len(session.attr.values))
	for key, value := range session.attr.values {
		result[key] = value
	}
	return result
}

// setUDPReadDeadline 设置 UDP 会话读超时时间。
func (session *AppSession) setUDPReadDeadline(deadline time.Time) {
	session.udpReadDeadlineLocker.Lock()
	defer session.udpReadDeadlineLocker.Unlock()

	session.udpReadDeadline = deadline
}

// getUDPReadDeadline 获取 UDP 会话读超时时间。
func (session *AppSession) getUDPReadDeadline() time.Time {
	session.udpReadDeadlineLocker.RLock()
	defer session.udpReadDeadlineLocker.RUnlock()

	return session.udpReadDeadline
}
