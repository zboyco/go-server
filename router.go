package goserver

import (
	"fmt"
	"reflect"
	"strings"
)

type ActionFunc func(*AppSession, []byte) ([]byte, error)

// ActionModule 方法处理模块
type ActionModule interface {
	Root() string // 返回当前模块根路径
}

// RegisterModule 注册方法处理模块（命令路由）
func (server *Server) RegisterModule(m ActionModule) error {
	mType := reflect.TypeOf(m)
	mValue := reflect.ValueOf(m)

	prefix := fmt.Sprintf("/%s", m.Root())
	prefix = strings.ReplaceAll(prefix, "//", "/")
	if prefix[len(prefix)-1] == '/' {
		prefix = prefix[:len(prefix)-1]
	}

	var (
		beforeAction Middlewares
		afterAction  Middlewares
	)

	if middlewaresBeforeAction, ok := m.(MiddlewaresBeforeAction); ok {
		beforeAction = middlewaresBeforeAction.MiddlewaresBeforeAction()
	}
	if middlewaresAfterAction, ok := m.(MiddlewaresAfterAction); ok {
		afterAction = middlewaresAfterAction.MiddlewaresAfterAction()
	}

	for i := 0; i < mType.NumMethod(); i++ {
		tem := mValue.Method(i).Interface()
		if temFunc, ok := tem.(func(*AppSession, []byte) ([]byte, error)); ok {
			funcName := fmt.Sprintf("%s/%s", prefix, mType.Method(i).Name)
			actions := make([]ActionFunc, 0)
			if beforeAction != nil {
				actions = append(actions, beforeAction...)
			}
			actions = append(actions, temFunc)
			if afterAction != nil {
				actions = append(actions, afterAction...)
			}
			err := server.Action(strings.ToLower(funcName), actions...)
			if err != nil {
				return fmt.Errorf("%s => %s", funcName, err.Error())
			}
		}
	}
	return nil
}

// hookAction 调用action
func (server *Server) hookAction(funcName string, session *AppSession, token []byte) error {
	funcName = strings.ToLower(funcName)
	actions, exist := server.actions[funcName]
	if !exist {
		return ActionNotFoundError
	}
	var err error
	if server.middlewaresBefore != nil {
		for i := range server.middlewaresBefore {
			token, err = server.middlewaresBefore[i](session, token)
			if err != nil {
				return err
			}
		}
	}
	for i := range actions {
		token, err = actions[i](session, token)
		if err != nil {
			return err
		}
	}
	if server.middlewaresAfter != nil {
		for i := range server.middlewaresAfter {
			token, err = server.middlewaresAfter[i](session, token)
			if err != nil {
				return err
			}
		}
	}
	if token != nil {
		session.Send(token)
	}
	return nil
}

// Action 添加单个Action
func (server *Server) Action(path string, actionFunc ...ActionFunc) error {
	if path == "" || path[0] != '/' {
		return PathFormatError
	}
	if _, exist := server.actions[path]; exist {
		return ActionConflictError
	}
	server.actions[path] = actionFunc
	return nil
}

type Middlewares []ActionFunc

type MiddlewaresBeforeAction interface {
	MiddlewaresBeforeAction() Middlewares
}

type MiddlewaresAfterAction interface {
	MiddlewaresAfterAction() Middlewares
}
