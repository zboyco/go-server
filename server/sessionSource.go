package server

import (
	"log"
	"sync"
	"time"
)

type sessionPool struct {
	source sync.Map            // 会话池
	list   chan *sessionHandle // 注册会话的通道
	count  int                 // 计数器
}

type sessionHandle struct {
	session *AppSession
	isAdd   bool
}

// addSession 添加会话到池中
func (s *sessionPool) addSession(session *AppSession) {
	s.list <- &sessionHandle{
		session,
		true,
	}
}

// deleteSession 移除Session
func (s *sessionPool) deleteSession(session *AppSession) {
	s.list <- &sessionHandle{
		session,
		false,
	}
}

// sessionPoolManager 会话池管理
func (s *sessionPool) sessionPoolManager() {
	for {
		m, ok := <-s.list

		if !ok {
			log.Println("Session池通道关闭")
			return
		}
		// 加入池
		if m.isAdd {
			s.source.Store(m.session.ID, m.session)
			s.count++
		} else {
			s.source.Delete(m.session.ID)
			s.count--
		}
	}
}

// clearTimeoutSession 周期性清理闲置会话
func (s *sessionPool) clearTimeoutSession(timeoutSecond int, interval int) {
	var timeoutTime time.Time

	if interval == 0 {
		return
	}

	for {
		time.Sleep(time.Duration(interval) * time.Second)

		timeoutTime = time.Now().Add(-time.Duration(timeoutSecond) * time.Second)
		{
			s.source.Range(func(key, value interface{}) bool {
				session := value.(*AppSession)
				if session.activeDateTime.Before(timeoutTime) {
					// 关闭连接
					go session.Close("超时")
				}
				return true
			})
		}
	}
}

// Count 返回会话池数量
func (s *sessionPool) Count() int {
	return s.count
}
