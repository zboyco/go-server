package goserver

import (
	"fmt"
	"reflect"
	"strings"
)

// ActionModule 方法处理模块
type ActionModule interface {
	Root() string // 返回当前模块根路径
}

// RegisterAction 注册方法处理模块（命令路由）
func (server *Server) RegisterAction(m ActionModule) error {
	mType := reflect.TypeOf(m)
	mValue := reflect.ValueOf(m)

	prefix := fmt.Sprintf("/%s", m.Root())
	prefix = strings.ReplaceAll(prefix, "//", "/")
	if prefix[len(prefix)-1] == '/' {
		prefix = prefix[:len(prefix)-1]
	}

	for i := 0; i < mType.NumMethod(); i++ {
		tem := mValue.Method(i).Interface()
		if temFunc, ok := tem.(func(*AppSession, []byte)); ok {
			funcName := fmt.Sprintf("%s/%s", prefix, mType.Method(i).Name)
			err := server.Action(strings.ToLower(funcName), temFunc)
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
	if _, exist := server.actions[funcName]; !exist {
		return ActionNotFoundError
	}
	server.actions[funcName](session, token)
	return nil
}

// Action 添加单个Action
func (server *Server) Action(path string, actionFunc func(client *AppSession, msg []byte)) error {
	if path == "" || path[0] != '/' {
		return PathFormatError
	}
	if _, exist := server.actions[path]; exist {
		return ActionConflictError
	}
	server.actions[path] = actionFunc
	return nil
}
