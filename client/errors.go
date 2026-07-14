package client

import "errors"

var (
	// ErrNotConnected 表示客户端尚未建立连接或连接已经被 Close 清除。
	ErrNotConnected = errors.New("client is not connected")
)
