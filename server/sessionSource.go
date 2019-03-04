package server

import (
	"log"
	"sync"
	"time"
)

type sessionSource struct {
	source map[int64]*AppSession // Seesion池
	list   chan *AppSession      // 注册Session的通道
	mutex  sync.Mutex            // 锁
}

// registerSession 注册session
func (s *sessionSource) registerSession() {
	for {
		session, ok := <-s.list

		if !ok {
			log.Println("Session池通道关闭")
			return
		}
		// 加入池
		s.mutex.Lock()
		s.source[session.ID] = session
		s.mutex.Unlock()
	}
}

// clearTimeoutSession 周期性清理闲置Seesion
func (s *sessionSource) clearTimeoutSession(timeoutSecond int, interval int) {
	var currentTime time.Time

	if interval == 0 {
		return
	}

	for {
		time.Sleep(time.Duration(interval) * time.Second)

		currentTime = time.Now()
		s.mutex.Lock()
		{
			for key, session := range s.source {
				if session.activeDateTime.Add(time.Duration(timeoutSecond) * time.Second).Before(currentTime) {
					log.Println("客户端[", key, "]超时关闭")
					session.Close("Timeout")
				}
			}
		}
		s.mutex.Unlock()
	}
}
