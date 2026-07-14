package goserver

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
)

// serveTCP 创建并独占 TCP accept 循环，为每条准入连接启动受 WaitGroup 跟踪的 handler。
//
// 临时 accept 错误采用最长一秒的指数退避；连接数达到上限时，新连接会立即关闭且不创建会话。
func (server *Server) serveTCP() error {
	var (
		listener net.Listener
		err      error
	)
	if server.config.tlsConfig == nil {
		listener, err = net.Listen("tcp", server.config.address)
	} else {
		listener, err = tls.Listen("tcp", server.config.address, server.config.tlsConfig)
	}
	if err != nil {
		return fmt.Errorf("listen TCP %s: %w", server.config.address, err)
	}
	if !server.setTCPListener(listener) {
		_ = listener.Close()
		return ErrServerClosed
	}
	defer listener.Close()
	server.startRouteLogging()

	var connectionSlots chan struct{}
	if server.config.maxConnections > 0 {
		connectionSlots = make(chan struct{}, server.config.maxConnections)
	}
	var temporaryDelay time.Duration
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if server.isStopRequested() || errors.Is(acceptErr, net.ErrClosed) {
				return acceptErr
			}
			if networkErr, ok := acceptErr.(net.Error); ok && networkErr.Temporary() {
				if temporaryDelay == 0 {
					temporaryDelay = 5 * time.Millisecond
				} else {
					temporaryDelay *= 2
				}
				if temporaryDelay > time.Second {
					temporaryDelay = time.Second
				}
				select {
				case <-time.After(temporaryDelay):
					continue
				case <-server.ctx.Done():
					return server.ctx.Err()
				}
			}
			return fmt.Errorf("accept TCP: %w", acceptErr)
		}
		temporaryDelay = 0

		if connectionSlots != nil {
			select {
			case connectionSlots <- struct{}{}:
			default:
				_ = conn.Close()
				continue
			}
		}
		server.handlerWG.Add(1)
		go func() {
			defer server.handlerWG.Done()
			if connectionSlots != nil {
				defer func() { <-connectionSlots }()
			}
			server.handleTCPClient(conn)
		}()
	}
}

// handleTCPClient 完成单条 TCP 连接的握手、过滤、会话注册、拆包和 action 分发。
//
// TLS 握手先于连接过滤器和用户回调执行。该函数退出时会幂等关闭会话；用户 panic 被隔离为运行错误。
func (server *Server) handleTCPClient(conn net.Conn) {
	defer func() {
		if recovered := recover(); recovered != nil {
			server.handleOnError(fmt.Errorf("TCP handler panic: %v", recovered))
			_ = conn.Close()
		}
	}()
	if err := server.handshakeTCP(conn); err != nil {
		if !server.isStopRequested() {
			server.handleOnError(fmt.Errorf("TLS handshake from %s: %w", conn.RemoteAddr(), err))
		}
		_ = conn.Close()
		return
	}

	for _, connectionFilter := range server.connectionFilterTCP {
		if connectionFilter == nil {
			continue
		}
		if err := connectionFilter(conn); err != nil {
			_ = conn.Close()
			return
		}
	}

	session := newTCPSession(server, uuid.NewString(), conn)
	storedSession, created := server.admitSession(session)
	if !created || storedSession != session {
		session.forceClose("session admission rejected")
		return
	}
	defer session.Close("TCP handler exited")
	server.stats.acceptedConnections.Add(1)
	server.safeNewSession(session)
	if session.Closed() || session.Context().Err() != nil {
		return
	}

	scanner := bufio.NewScanner(conn)
	initialBuffer := 4 * 1024
	if server.config.maxFrameSize < initialBuffer {
		initialBuffer = server.config.maxFrameSize
	}
	scanner.Buffer(make([]byte, 0, initialBuffer), server.config.maxFrameSize)
	scanner.Split(server.splitFunc)
	if err := server.touchReadDeadline(conn); err != nil {
		server.handleOnError(formatSessionError(session, "set read deadline", err))
		return
	}

	for scanner.Scan() {
		if err := server.touchReadDeadline(conn); err != nil {
			server.handleOnError(formatSessionError(session, "refresh read deadline", err))
			return
		}
		token := scanner.Bytes()
		server.stats.bytesRead.Add(uint64(len(token)))
		actionName := ""
		if server.resolveAction != nil {
			var err error
			actionName, token, err = server.resolveAction(token)
			if err != nil {
				server.stats.protocolErrors.Add(1)
				server.handleOnError(formatSessionError(session, "decode frame", err))
				return
			}
		}
		if err := server.hookAction(session.Context(), actionName, session, token); err != nil {
			server.handleOnError(formatSessionError(session, "dispatch action", err))
		}
		if session.Context().Err() != nil {
			return
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		// 主动关闭会通过底层传输错误解除 Scanner 阻塞，此类预期错误不再重复上报。
		if !server.isStopRequested() && session.Context().Err() == nil {
			server.handleOnError(formatSessionError(session, "scan TCP", scanErr))
		}
		_ = session.Close(scanErr.Error())
	}
}

// handshakeTCP 在连接过滤器和会话回调执行前完成 TLS 握手。
// 非 TLS 连接直接返回 nil；握手同时受服务端 context 和可选握手超时约束。
func (server *Server) handshakeTCP(conn net.Conn) error {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil
	}
	ctx := server.ctx
	if timeout := server.config.tlsHandshakeTimeout; timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return tlsConn.HandshakeContext(ctx)
}

// logRoutes 以 Debug 级别记录已注册路由及监听元数据，避免默认日志污染正常输出。
func (server *Server) logRoutes() {
	for source, routes := range server.routers {
		for _, route := range routes {
			server.config.logger.Debug("registered route", "source", source, "path", route[0], "method", route[1])
		}
	}
	server.config.logger.Debug("server listening", "network", server.config.network, "address", server.config.address)
}

// startRouteLogging 在受 drain 跟踪的 goroutine 中记录启动信息，避免自定义 logger 阻塞 Serve。
func (server *Server) startRouteLogging() {
	server.workerWG.Add(1)
	go func() {
		defer server.workerWG.Done()
		// 自定义 slog.Handler 的 panic 不能因异步化而升级为进程级崩溃。
		defer func() { _ = recover() }()
		server.logRoutes()
	}()
}
