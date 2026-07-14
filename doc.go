// Package goserver 提供面向应用层协议的 TCP/UDP 服务端能力。
//
// 包内包含基于 bufio.SplitFunc 的拆包、命令路由、中间件、并发安全会话、
// TLS、空闲回收、受控停机和运行统计。每个 Server 只支持一次 Serve 生命周期；
// Serve 返回表示监听循环已经退出，完整排空由 Shutdown、Close 或 WaitDrained 等待。
// 所有路由、过滤器和回调都应在 Serve 前完成注册。
package goserver
