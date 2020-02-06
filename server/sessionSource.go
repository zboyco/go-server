package server

import (
	"log"
	"sync"
	"time"
)

type sessionSource struct {
	source     map[string]*AppSession // 会话池
	list       chan *AppSession       // 注册会话的通道
	sync.Mutex                        // 锁
}

// addSession 添加会话到池中
func (s *sessionSource) addSession(session *AppSession) {
	s.list <- session
}

// registerSession 注册会话
func (s *sessionSource) registerSession() {
	for {
		session, ok := <-s.list

		if !ok {
			log.Println("Session池通道关闭")
			return
		}
		// 加入池
		s.Lock()
		s.source[session.ID] = session
		s.Unlock()
	}
}

// clearTimeoutSession 周期性清理闲置会话
func (s *sessionSource) clearTimeoutSession(timeoutSecond int, interval int) {
	var timeoutTime time.Time

	if interval == 0 {
		return
	}

	for {
		time.Sleep(time.Duration(interval) * time.Second)

		timeoutTime = time.Now().Add(-time.Duration(timeoutSecond) * time.Second)
		s.Lock()
		{
			for key, session := range s.source {
				if session.activeDateTime.Before(timeoutTime) {
					// 关闭连接
					session.Close("超时")
					// 移出会话池
					delete(s.source, key)
				}
			}
		}
		s.Unlock()
	}
}

// deleteSession 移除Session
func (s *sessionSource) deleteSession(sessionID string) {
	s.Lock()
	defer s.Unlock()
	delete(s.source, sessionID)
}

// Count 返回会话池数量
func (s *sessionSource) Count() int {
	return len(s.source)
}
