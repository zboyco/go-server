package goserver

import "errors"

var (
	// ErrServerRunning 表示服务已经启动，不能再次 Serve 或修改启动前配置。
	ErrServerRunning = errors.New("server is running")
	// ErrServerClosed 表示单次 Server 生命周期已经结束，不能重新启动。
	ErrServerClosed = errors.New("server is closed")
	// ErrInvalidConfig 表示构造选项、回调、路由或中间件配置不合法。
	ErrInvalidConfig = errors.New("invalid server config")
	// ErrUnsupportedNetwork 表示传入的传输层不是 TCP 或 UDP。
	ErrUnsupportedNetwork = errors.New("unsupported network")
	// ErrSessionClosed 表示服务端会话已经开始关闭，无法继续执行操作。
	ErrSessionClosed = errors.New("session is closed")
	// ErrSessionNotFound 表示指定 ID 当前没有对应的开放会话。
	ErrSessionNotFound = errors.New("session not found")
	// ErrUDPQueueFull 表示 queued 模式目标 UDP worker 的有界队列已满。
	ErrUDPQueueFull = errors.New("UDP processing queue is full")
	// ErrDatagramTooLarge 表示 UDP 数据报超过配置的最大允许大小。
	ErrDatagramTooLarge = errors.New("UDP datagram exceeds configured maximum")
	// ErrInvalidFrame 表示拆包器返回非法推进量、帧不完整或帧超过限制。
	ErrInvalidFrame = errors.New("invalid frame")
	// ErrPathFormat 表示显式路由没有以“/”开头。
	ErrPathFormat = errors.New("path must start with \"/\"")
	// ErrActionNotFound 表示消息解析出的 action 路径没有对应处理器。
	ErrActionNotFound = errors.New("action does not exist")
	// ErrActionConflict 表示同一规范化路由被重复注册。
	ErrActionConflict = errors.New("action registration conflict")
	// ErrModuleFormat 表示模块不是结构体或结构体指针。
	ErrModuleFormat = errors.New("module must be a struct or pointer to struct")
)
