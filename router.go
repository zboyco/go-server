package go_server

import (
	"errors"
	"fmt"
	"reflect"
)

// Action 方法处理模块
type Action interface {
	ReturnRootPath() string // 返回当前模块根路径
}

// RegisterAction 注册方法处理模块
func (server *Server) RegisterAction(m Action) error {
	mType := reflect.TypeOf(m)
	mValue := reflect.ValueOf(m)

	prefix := m.ReturnRootPath()
	if prefix != "" {
		prefix = "/" + prefix
	}

	for i := 0; i < mType.NumMethod(); i++ {
		tem := mValue.Method(i).Interface()
		if temFunc, ok := tem.(func(*AppSession, []byte)); ok {
			funcName := fmt.Sprintf("%s/%s", prefix, mType.Method(i).Name)
			if _, exist := server.actions[funcName]; exist {
				return errors.New(fmt.Sprintf("action %s already exist", funcName))
			}
			server.actions[funcName] = temFunc
		}
	}
	return nil
}

// hookAction 调用action
func (server *Server) hookAction(funcName string, session *AppSession, token []byte) error {
	if _, exist := server.actions[funcName]; !exist {
		return errors.New("action not exist")
	}
	server.actions[funcName](session, token)
	return nil
}
