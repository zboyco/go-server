package go_server

import (
	"fmt"
	"reflect"
)

type action interface {
	ReturnPath() string
}

func (server *Server) RegisterAction(m action) {
	mType := reflect.TypeOf(m)
	mValue := reflect.ValueOf(m)

	for i := 0; i < mType.NumMethod(); i++ {
		tem := mValue.Method(i).Interface()
		if temFunc, ok := tem.(func(*AppSession, []byte)); ok {
			server.actions[fmt.Sprintf("%s/%s", m.ReturnPath(), mType.Method(i).Name)] = temFunc
		}
	}
}
