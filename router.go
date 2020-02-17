package goserver

import (
	"errors"
	"fmt"
	"reflect"
)

// ActionModule 方法处理模块
type ActionModule interface {
	ReturnRootPath() string // 返回当前模块根路径
}

// RegisterAction 注册方法处理模块（命令路由）
func (server *Server) RegisterAction(m ActionModule) error {
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
			err := server.Action(funcName, temFunc)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// hookAction 调用action
func (server *Server) hookAction(funcName string, session *AppSession, token []byte) error {
	if _, exist := server.actions[funcName]; !exist {
		return errors.New(fmt.Sprintf("action \"%v\" not exist", funcName))
	}
	server.actions[funcName](session, token)
	return nil
}

// Action 添加单个Action
func (server *Server) Action(path string, actionFunc func(client *AppSession, msg []byte)) error {
	if path == "" || path[0] != '/' {
		return errors.New("path must start with '/'")
	}
	if _, exist := server.actions[path]; exist {
		return errors.New(fmt.Sprintf("action %s already exist", path))
	}
	server.actions[path] = actionFunc
	return nil
}
