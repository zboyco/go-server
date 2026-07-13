package goserver

import (
	"errors"
	"log/slog"
	"sync"
)

// sessionPool 会话管理池
type sessionPool struct {
	pool    sync.Map            // 会话池
	list    chan *sessionHandle // 注册会话的通道
	counter int                 // 计数器
	active  bool                // 是否接收会话变更
	mu      sync.Mutex          // 保护计数器和 active 状态
}

// sessionHandle 会话管理操作
type sessionHandle struct {
	session *AppSession // 会话
	isAdd   bool        // 是否添加到池，false为从池中移除
}

// addSession 添加会话到池中
func (s *sessionPool) addSession(session *AppSession) {
	if !s.isActive() {
		return
	}
	s.list <- &sessionHandle{
		session,
		true,
	}
}

// deleteSession 移除Session
func (s *sessionPool) deleteSession(session *AppSession) {
	if !s.isActive() {
		return
	}
	s.list <- &sessionHandle{
		session,
		false,
	}
}

// sessionPoolManager 会话池管理
func (s *sessionPool) sessionPoolManager(stop <-chan struct{}) {
	for {
		select {
		case m, ok := <-s.list:
			if !ok {
				slog.Warn("session pool channel closed")
				return
			}
			s.handleSession(m)
		case <-stop:
			return
		}
	}
}

func (s *sessionPool) handleSession(m *sessionHandle) {
	if !s.isActive() {
		return
	}

	// 加入池
	if m.isAdd {
		s.storeSession(m.session)
	} else {
		if current, exist := s.pool.Load(m.session.ID); exist && current == m.session {
			s.pool.Delete(m.session.ID)
			s.mu.Lock()
			s.counter--
			s.mu.Unlock()
		}
	}
}

// storeSession 同步注册会话；返回实际保存在池中的会话和是否新建成功。
func (s *sessionPool) storeSession(session *AppSession) (*AppSession, bool) {
	if !s.isActive() {
		return nil, false
	}

	current, loaded := s.pool.LoadOrStore(session.ID, session)
	if !loaded {
		s.mu.Lock()
		s.counter++
		s.mu.Unlock()
		return session, true
	}

	currentSession, ok := current.(*AppSession)
	if !ok {
		return nil, false
	}
	if currentSession.isClosed() {
		s.pool.Store(session.ID, session)
		return session, true
	}
	return currentSession, false
}

func (s *sessionPool) start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.active = true
}

func (s *sessionPool) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.active = false
}

func (s *sessionPool) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.active
}

func (s *sessionPool) clear() {
	s.pool.Range(func(key, value interface{}) bool {
		s.pool.Delete(key)
		return true
	})

	s.mu.Lock()
	s.counter = 0
	s.mu.Unlock()
}

// 返回会话池数量
func (s *sessionPool) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.counter
}

// 通过ID获取会话
func (s *sessionPool) getSessionByID(id string) (*AppSession, error) {
	if session, ok := s.pool.Load(id); ok {
		return session.(*AppSession), nil
	}
	return nil, errors.New("not found session")
}

// 属性条件判断方法
type ConditionFunc func(map[string]interface{}) bool

// 通过属性获取会话
func (s *sessionPool) getSessionByAttr(cond ConditionFunc) <-chan *AppSession {
	result := make(chan *AppSession)
	go func() {
		defer close(result)
		s.pool.Range(func(id, sessionInterface interface{}) bool {
			session := sessionInterface.(*AppSession)
			if cond(session.attrSnapshot()) {
				result <- session
			}
			return true
		})
	}()
	return result
}

// 获取所有会话
func (s *sessionPool) getAllSessions() <-chan *AppSession {
	result := make(chan *AppSession)
	go func() {
		defer close(result)
		s.pool.Range(func(key, value interface{}) bool {
			result <- value.(*AppSession)
			return true
		})
	}()
	return result
}
