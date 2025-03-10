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

type ActionSummary interface {
	Summary() string // 返回当前模块描述
}

// RegisterModule 注册方法处理模块（命令路由）
func (server *Server) RegisterModule(m ActionModule) error {
	if server.running {
		return ErrServerRunning
	}

	mType := reflect.TypeOf(m)
	mValue := reflect.ValueOf(m)

	structPath := mType.Elem().String()

	if summary, ok := m.(ActionSummary); ok {
		structPath = fmt.Sprintf("%s %s", structPath, summary.Summary())
	}

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
			method := mType.Method(i)
			callPath := strings.ToLower(fmt.Sprintf("%s/%s", prefix, method.Name))
			actions := make([]ActionFunc, 0)
			if beforeAction != nil {
				actions = append(actions, beforeAction...)
			}
			actions = append(actions, temFunc)
			if afterAction != nil {
				actions = append(actions, afterAction...)
			}
			err := server.action(callPath, structPath, method.Name, actions...)
			if err != nil {
				return fmt.Errorf("%s => %s", callPath, err.Error())
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
		return ErrActionNotFound
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
		return session.Send(token)
	}
	return nil
}

// Action 添加单个Action
func (server *Server) Action(path string, actionFunc ...ActionFunc) error {
	if server.running {
		return ErrServerRunning
	}

	return server.action(path, ".", "", actionFunc...)
}

func (server *Server) action(path, structPath, methodName string, actionFunc ...ActionFunc) error {
	if path == "" || path[0] != '/' {
		return ErrPathFormat
	}
	if _, exist := server.actions[path]; exist {
		return ErrActionConflict
	}
	server.actions[path] = actionFunc

	// 生成路由
	if _, exist := server.routers[structPath]; !exist {
		server.routers[structPath] = make([][]string, 0)
	}
	server.routers[structPath] = append(server.routers[structPath], []string{path, methodName})
	return nil
}

type Middlewares []ActionFunc

type MiddlewaresBeforeAction interface {
	MiddlewaresBeforeAction() Middlewares
}

type MiddlewaresAfterAction interface {
	MiddlewaresAfterAction() Middlewares
}
