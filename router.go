package go_server

import (
	"errors"
	"fmt"
	"reflect"
)

type action interface {
	ReturnPath() string
}

func (server *Server) RegisterAction(m action) error {
	mType := reflect.TypeOf(m)
	mValue := reflect.ValueOf(m)

	prefix := m.ReturnPath()
	if prefix != "" {
		prefix += "/"
	}

	for i := 0; i < mType.NumMethod(); i++ {
		tem := mValue.Method(i).Interface()
		if temFunc, ok := tem.(func(*AppSession, []byte)); ok {
			funcName := fmt.Sprintf("%s%s", prefix, mType.Method(i).Name)
			if _, exist := server.actions[funcName]; exist {
				return errors.New("action already exist")
			}
			server.actions[funcName] = temFunc
		}
	}
	return nil
}

func (server *Server) HookAction(funcName string, session *AppSession, token []byte) error {
	if _, exist := server.actions[funcName]; !exist {
		return errors.New("action not exist")
	}
	server.actions[funcName](session, token)
	return nil
}
