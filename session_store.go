package goserver

import (
	"sync"
	"sync/atomic"
)

// sessionStore 并发保存活跃会话及其原子计数，不创建后台生产 goroutine。
type sessionStore struct {
	values        sync.Map
	residentCount atomic.Int64
	openCount     atomic.Int64
}

// store 新增会话，或用新实例替换同 ID 的已关闭会话。
// 返回实际存储的会话及是否由本次调用创建；已有活跃会话不会被覆盖。
func (store *sessionStore) store(session *AppSession) (*AppSession, bool) {
	session.inactiveTrigger = func() {
		store.deactivate(session)
	}
	for {
		current, loaded := store.values.LoadOrStore(session.ID, session)
		if !loaded {
			store.residentCount.Add(1)
			store.activate(session)
			return session, true
		}
		currentSession, ok := current.(*AppSession)
		if !ok {
			session.inactiveTrigger = nil
			return nil, false
		}
		if !currentSession.Closed() {
			if currentSession == session {
				return currentSession, false
			}
			session.inactiveTrigger = nil
			return currentSession, false
		}
		if store.values.CompareAndSwap(session.ID, currentSession, session) {
			store.activate(session)
			return session, true
		}
	}
}

// activate 把成功存储的会话计入公开活跃数量，并补偿发布后立即关闭的竞态。
func (store *sessionStore) activate(session *AppSession) {
	store.openCount.Add(1)
	session.activeCounted.Store(true)
	if session.Closed() {
		store.deactivate(session)
	}
}

// deactivate 在会话首次进入 closing 时把它从公开活跃数量中移除。
func (store *sessionStore) deactivate(session *AppSession) {
	if session.activeCounted.CompareAndSwap(true, false) {
		store.openCount.Add(-1)
	}
}

// delete 仅在 session 仍是该 ID 的当前值时删除它，避免旧会话关闭时误删已替换的新会话。
func (store *sessionStore) delete(session *AppSession) bool {
	if store.values.CompareAndDelete(session.ID, session) {
		store.residentCount.Add(-1)
		return true
	}
	return false
}

// get 根据 ID 返回当前保存的会话及存在标记。
func (store *sessionStore) get(id string) (*AppSession, bool) {
	value, ok := store.values.Load(id)
	if !ok {
		return nil, false
	}
	session, ok := value.(*AppSession)
	return session, ok
}

// sessions 同步构造开放会话的稳定切片快照，不暴露已经开始关闭的会话。
func (store *sessionStore) sessions() []*AppSession {
	result := make([]*AppSession, 0, store.openCount.Load())
	store.values.Range(func(_, value any) bool {
		if session, ok := value.(*AppSession); ok && !session.Closed() {
			result = append(result, session)
		}
		return true
	})
	return result
}

// all 返回包含 closing 会话的内部快照，供中断传输和最终清理使用。
func (store *sessionStore) all() []*AppSession {
	result := make([]*AppSession, 0, store.residentCount.Load())
	store.values.Range(func(_, value any) bool {
		if session, ok := value.(*AppSession); ok {
			result = append(result, session)
		}
		return true
	})
	return result
}

// find 返回属性快照满足 cond 的会话；cond 在 sync.Map 内部锁之外使用独立属性 map 执行。
func (store *sessionStore) find(cond ConditionFunc) []*AppSession {
	if cond == nil {
		return nil
	}
	result := make([]*AppSession, 0)
	store.values.Range(func(_, value any) bool {
		session, ok := value.(*AppSession)
		if ok && !session.Closed() && cond(session.attrSnapshot()) {
			result = append(result, session)
		}
		return true
	})
	return result
}

// len 以 O(1) 返回当前开放会话数；closing 会话仍可留在内部 map 直到最终回调完成。
func (store *sessionStore) len() int {
	return int(store.openCount.Load())
}

// clear 在停机清理完成后删除存储中的全部会话并归零计数。
func (store *sessionStore) clear() {
	store.values.Range(func(key, value any) bool {
		if session, ok := value.(*AppSession); ok {
			session.activeCounted.Store(false)
		}
		store.values.Delete(key)
		return true
	})
	store.residentCount.Store(0)
	store.openCount.Store(0)
}

// ConditionFunc 根据隔离的会话属性快照筛选会话。
// 修改传入 map 不会影响真实会话属性，但 map 中引用类型的值仍由调用方负责并发安全。
type ConditionFunc func(map[string]any) bool
