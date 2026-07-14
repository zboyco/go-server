package goserver

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// ActionFunc 处理一个已完成拆包的消息。
//
// ctx 在会话或服务端开始关闭时取消，session 表示消息来源。返回非 nil 字节切片时服务端继续执行
// 后续中间件并同步回包；返回 nil 时不自动回包；返回 error 时立即终止后续处理。
// message 可能引用内部复用缓冲区，只在当前同步处理链返回前有效；异步持有前必须复制。
type ActionFunc func(context.Context, *AppSession, []byte) ([]byte, error)

// ActionModule 为通过反射注册的 action 方法提供路由根路径。
type ActionModule interface {
	Root() string
}

// ActionSummary 可选地为反射注册的模块提供调试摘要。
type ActionSummary interface {
	Summary() string
}

// routeRegistration 保存模块方法在提交到 Server 前的完整、已校验注册信息。
type routeRegistration struct {
	path       string
	structPath string
	methodName string
	actions    []ActionFunc
}

// RegisterModule 注册 module 上所有签名与 ActionFunc 一致的导出方法。
//
// module 必须是结构体或结构体指针并实现 ActionModule。方法名会转为小写并拼接 Root；
// 可选的模块 before/after 中间件会分别包裹每个方法。全部路由先统一检查冲突，再原子写入配置，
// 因此返回错误时不会留下部分注册结果。该方法只能在 Serve 前调用。
func (server *Server) RegisterModule(module ActionModule) error {
	if module == nil {
		return ErrModuleFormat
	}
	moduleType := reflect.TypeOf(module)
	moduleValue := reflect.ValueOf(module)
	if moduleValue.Kind() == reflect.Ptr && moduleValue.IsNil() {
		return ErrModuleFormat
	}
	if moduleType.Kind() == reflect.Ptr {
		if moduleType.Elem().Kind() != reflect.Struct {
			return ErrModuleFormat
		}
	} else if moduleType.Kind() != reflect.Struct {
		return ErrModuleFormat
	}

	structPath := moduleType.String()
	if moduleType.Kind() == reflect.Ptr {
		structPath = moduleType.Elem().String()
	}
	if summary, ok := module.(ActionSummary); ok {
		structPath = fmt.Sprintf("%s %s", structPath, summary.Summary())
	}
	prefix := strings.TrimSuffix(strings.ReplaceAll("/"+module.Root(), "//", "/"), "/")

	var (
		beforeAction Middlewares
		afterAction  Middlewares
		err          error
	)
	if provider, ok := module.(MiddlewaresBeforeAction); ok {
		beforeAction, err = cloneMiddlewares(provider.MiddlewaresBeforeAction())
		if err != nil {
			return fmt.Errorf("module before middleware: %w", err)
		}
	}
	if provider, ok := module.(MiddlewaresAfterAction); ok {
		afterAction, err = cloneMiddlewares(provider.MiddlewaresAfterAction())
		if err != nil {
			return fmt.Errorf("module after middleware: %w", err)
		}
	}

	registrations := make([]routeRegistration, 0)
	for index := 0; index < moduleType.NumMethod(); index++ {
		methodValue := moduleValue.Method(index).Interface()
		action, ok := methodValue.(func(context.Context, *AppSession, []byte) ([]byte, error))
		if !ok {
			continue
		}
		method := moduleType.Method(index)
		actions := make([]ActionFunc, 0, len(beforeAction)+1+len(afterAction))
		actions = append(actions, beforeAction...)
		actions = append(actions, action)
		actions = append(actions, afterAction...)
		registrations = append(registrations, routeRegistration{
			path:       strings.ToLower(fmt.Sprintf("%s/%s", prefix, method.Name)),
			structPath: structPath,
			methodName: method.Name,
			actions:    actions,
		})
	}

	return server.configure(func() error {
		registeredPaths := make(map[string]struct{}, len(registrations))
		for _, registration := range registrations {
			if _, exists := registeredPaths[registration.path]; exists {
				return fmt.Errorf("%s: %w", registration.path, ErrActionConflict)
			}
			registeredPaths[registration.path] = struct{}{}
			if _, exists := server.actions[registration.path]; exists {
				return fmt.Errorf("%s: %w", registration.path, ErrActionConflict)
			}
		}
		for _, registration := range registrations {
			server.actionLocked(registration.path, registration.structPath, registration.methodName, registration.actions)
		}
		return nil
	})
}

// hookAction 按固定顺序执行全局 before、路由 action 链、全局 after，并在结果非 nil 时回包。
// 任一步骤返回 error 都会停止后续处理；路由查找不区分大小写。
func (server *Server) hookAction(ctx context.Context, path string, session *AppSession, token []byte) error {
	actions, exists := server.actions[strings.ToLower(path)]
	if !exists {
		return ErrActionNotFound
	}
	var err error
	for _, middleware := range server.middlewaresBefore {
		token, err = middleware(ctx, session, token)
		if err != nil {
			return err
		}
	}
	for _, action := range actions {
		token, err = action(ctx, session, token)
		if err != nil {
			return err
		}
	}
	for _, middleware := range server.middlewaresAfter {
		token, err = middleware(ctx, session, token)
		if err != nil {
			return err
		}
	}
	if token != nil {
		return session.sendResponse(ctx, token)
	}
	return nil
}

// Action 在 Serve 启动前注册一条显式路由及其有序 action 链。
//
// path 必须以“/”开头，匹配时不区分大小写；actions 不能为空且不能包含 nil。
// 重复路径返回 ErrActionConflict。传入切片会被复制，调用方后续修改不会影响 Server。
func (server *Server) Action(path string, actions ...ActionFunc) error {
	if path == "" || path[0] != '/' {
		return ErrPathFormat
	}
	if len(actions) == 0 {
		return fmt.Errorf("%w: route requires at least one action", ErrInvalidConfig)
	}
	for _, action := range actions {
		if action == nil {
			return fmt.Errorf("%w: nil action", ErrInvalidConfig)
		}
	}
	return server.configure(func() error {
		path = strings.ToLower(path)
		if _, exists := server.actions[path]; exists {
			return ErrActionConflict
		}
		server.actionLocked(path, ".", "", actions)
		return nil
	})
}

// actionLocked 在调用方已持有 lifecycleMu 时保存已校验路由，并隔离调用方持有的 action 切片。
func (server *Server) actionLocked(path, structPath, methodName string, actions []ActionFunc) {
	server.actions[path] = append([]ActionFunc(nil), actions...)
	server.routers[structPath] = append(server.routers[structPath], []string{path, methodName})
}

// Middlewares 表示按切片顺序执行的 action 中间件流水线。
type Middlewares []ActionFunc

// cloneMiddlewares 校验中间件不存在 nil 元素，并复制调用方拥有的切片。
func cloneMiddlewares(middlewares Middlewares) (Middlewares, error) {
	for index, middleware := range middlewares {
		if middleware == nil {
			return nil, fmt.Errorf("%w: nil middleware at index %d", ErrInvalidConfig, index)
		}
	}
	return append(Middlewares(nil), middlewares...), nil
}

// MiddlewaresBeforeAction 由模块可选实现，用于提供每个模块 action 前执行的本地中间件。
type MiddlewaresBeforeAction interface {
	MiddlewaresBeforeAction() Middlewares
}

// MiddlewaresAfterAction 由模块可选实现，用于提供每个模块 action 后执行的本地中间件。
type MiddlewaresAfterAction interface {
	MiddlewaresAfterAction() Middlewares
}
