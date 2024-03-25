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
}

// sessionHandle 会话管理操作
type sessionHandle struct {
	session *AppSession // 会话
	isAdd   bool        // 是否添加到池，false为从池中移除
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
			slog.Warn("session pool channel closed")
			return
		}
		// 加入池
		if m.isAdd {
			s.pool.Store(m.session.ID, m.session)
			s.counter++
		} else {
			s.pool.Delete(m.session.ID)
			s.counter--
		}
	}
}

// 返回会话池数量
func (s *sessionPool) count() int {
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
			if cond(session.attr) {
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
