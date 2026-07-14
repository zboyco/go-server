package goserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/zboyco/go-server/filter"
)

// Network 表示服务端或客户端使用的传输层协议。
type Network string

const (
	// TCP 表示面向连接、按字节流传输的 TCP 协议。
	TCP Network = "tcp"
	// UDP 表示无连接、保留数据报边界的 UDP 协议。
	UDP Network = "udp"
)

// serverState 表示单次 Server 生命周期所处的内部状态。
type serverState uint8

const (
	serverNew serverState = iota
	serverStarting
	serverRunning
	serverStopping
	serverStopped
)

// Server 管理一次性的 TCP 或 UDP 服务生命周期。
//
// Server 的配置和路由只能在首次调用 Serve 前修改。Serve、Shutdown、Close、
// RequestShutdown、RequestClose、WaitDrained、Addr、Stats 及会话查询方法可由多个 goroutine 调用；
// action 与事件回调由服务端拥有的 goroutine 同步执行。
type Server struct {
	config   serverConfig
	sessions sessionStore
	stats    serverStats

	onError              func(error)
	onNewSessionRegister func(*AppSession)
	onSessionClosed      func(*AppSession, string)

	ioEOF               []byte
	connectionFilterTCP []filter.ConnectionFilterTCP
	connectionFilterUDP []filter.ConnectionFilterUDP
	splitFunc           bufio.SplitFunc
	resolveAction       filter.ResolveActionFunc
	middlewaresBefore   Middlewares
	middlewaresAfter    Middlewares
	sendPacketFilter    Middlewares
	actions             map[string][]ActionFunc
	routers             map[string][][]string

	lifecycleMu   sync.Mutex
	state         serverState
	stopRequested bool
	gracefulStop  bool
	stopReason    string
	ctx           context.Context
	cancel        context.CancelFunc
	forceCtx      context.Context
	forceCancel   context.CancelFunc
	tcpListener   net.Listener
	udpConn       *net.UDPConn
	serveDone     chan struct{}
	drained       chan struct{}
	serveDoneOnce sync.Once
	cleanupOnce   sync.Once
	finishOnce    sync.Once
	handlerWG     sync.WaitGroup
	workerWG      sync.WaitGroup
	udpRuntime    *udpRuntime
}

// New 创建并校验一个单次使用的 Server。
//
// network 仅支持 TCP 或 UDP，address 必须是标准库可解析的完整监听地址。
// options 按传入顺序应用；任一配置无效时返回可通过 errors.Is 判定的错误。
func New(network Network, address string, options ...Option) (*Server, error) {
	config, err := newServerConfig(network, address, options...)
	if err != nil {
		return nil, err
	}
	forceCtx, forceCancel := context.WithCancel(context.Background())
	return &Server{
		config:      config,
		splitFunc:   bufio.ScanLines,
		actions:     make(map[string][]ActionFunc),
		routers:     make(map[string][][]string),
		state:       serverNew,
		forceCtx:    forceCtx,
		forceCancel: forceCancel,
		serveDone:   make(chan struct{}),
		drained:     make(chan struct{}),
	}, nil
}

// NewTCP 创建监听 address 的 TCP Server，等价于 New(TCP, address, options...)。
func NewTCP(address string, options ...Option) (*Server, error) {
	return New(TCP, address, options...)
}

// NewUDP 创建监听 address 的 UDP Server，等价于 New(UDP, address, options...)。
func NewUDP(address string, options ...Option) (*Server, error) {
	return New(UDP, address, options...)
}

// Serve 创建监听器并阻塞，直到监听循环停止或发生错误。
//
// 一个 Server 只能执行一次 Serve。启动前必须至少注册一个普通消息 action 或命令路由；
// 重复启动分别返回 ErrServerRunning 或 ErrServerClosed。Serve 返回时清理流程已经启动，但 action、
// worker 和同步关闭回调可能仍在排空；需要等待完整清理时使用 Shutdown、Close 或 WaitDrained。
func (server *Server) Serve() error {
	if err := server.beginServe(); err != nil {
		return err
	}

	var serveErr error
	switch server.config.network {
	case TCP:
		serveErr = server.serveTCP()
	case UDP:
		serveErr = server.serveUDP()
	default:
		serveErr = fmt.Errorf("%w: %q", ErrUnsupportedNetwork, server.config.network)
	}

	controlled := server.isStopRequested()
	if !controlled {
		server.initiateStop(false, "server stopped")
	}
	server.startCleanup()
	server.markServeDone()

	if controlled && isExpectedCloseError(serveErr) {
		return nil
	}
	return serveErr
}

// Shutdown 发起优雅停机，并等待完整清理或 ctx 到期。
//
// 优雅停机会停止接收新流量、取消会话 context、尽力发送已配置的 EOF 标记、
// 关闭会话并等待服务端拥有的 handler 和 worker 退出。ctx 为 nil 时使用 context.Background。
// 若 ctx 先结束，剩余会话的传输会被中断；Shutdown 等待 Serve 的监听循环退出后返回 ctx.Err()，
// 但不会继续等待忽略 context 的用户 action、过滤器或同步回调。
//
// Shutdown 会等待 action 和同步事件回调退出，因此不能在这些回调内直接调用；
// 回调内部应使用不会等待当前 goroutine 的 RequestShutdown。
func (server *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	server.RequestShutdown()
	select {
	case <-server.drained:
		return nil
	case <-ctx.Done():
		server.interruptAllSessions()
		<-server.serveDone
		return ctx.Err()
	}
}

// WaitDrained 等待 Server 拥有的 handler、worker、关闭任务和同步回调全部退出。
// ctx 为 nil 时使用 context.Background；服务尚未停止时也可以调用。
func (server *Server) WaitDrained(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-server.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RequestShutdown 发起优雅停机，但不等待服务端拥有的 goroutine 退出。
// 该方法可安全地在 action、OnError、OnNewSessionRegister 或 OnSessionClosed 中同步调用。
func (server *Server) RequestShutdown() {
	server.initiateStop(true, "server shutdown")
}

// Close 立即关闭活跃会话，并等待监听循环及服务端拥有的 goroutine 退出。
//
// Close 不发送 EOF 标记。忽略 context 的 action、发送过滤器或同步事件回调可能阻止它返回；
// 回调内部应改用 RequestClose，并在服务器执行路径之外按需调用 WaitDrained。
func (server *Server) Close() error {
	server.RequestClose()
	<-server.drained
	return nil
}

// RequestClose 发起立即关闭，但不等待服务端拥有的 goroutine 退出。
// 该方法可安全地在 action 和服务端同步事件回调中调用；它不会发送 EOF 标记。
func (server *Server) RequestClose() {
	server.initiateStop(false, "server closed")
	server.interruptAllSessions()
}

// Addr 返回当前已绑定的监听地址。
// 在监听器创建前或停止请求撤下监听资源后返回 nil；可用于获取端口为 0 时由系统分配的实际地址。
func (server *Server) Addr() net.Addr {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	if server.tcpListener != nil {
		return server.tcpListener.Addr()
	}
	if server.udpConn != nil {
		return server.udpConn.LocalAddr()
	}
	return nil
}

// Stats 返回一次无锁读取的运行统计快照。
// 各字段独立原子读取，因此该快照适合监控，不保证所有字段对应完全相同的 CPU 时刻。
func (server *Server) Stats() Stats {
	return server.stats.snapshot()
}

// beginServe 校验启动前置条件并创建服务级 context。
// lifecycleMu 保证只有一个调用方能把状态从 serverNew 推进到 serverStarting。
func (server *Server) beginServe() error {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	if server.state == serverStopped {
		return ErrServerClosed
	}
	if server.state != serverNew {
		return ErrServerRunning
	}
	if len(server.actions) == 0 {
		return fmt.Errorf("%w: at least one action is required", ErrInvalidConfig)
	}
	server.ctx, server.cancel = context.WithCancel(context.Background())
	server.state = serverStarting
	return nil
}

// setTCPListener 在仍处于启动状态时发布 TCP 监听器并进入运行态。
// 若停止请求先到达，则返回 false，由调用方关闭尚未发布的 listener。
func (server *Server) setTCPListener(listener net.Listener) bool {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	if server.state != serverStarting {
		return false
	}
	server.tcpListener = listener
	server.state = serverRunning
	return true
}

// setUDPConn 在仍处于启动状态时发布 UDP socket 并进入运行态。
// 若停止请求先到达，则返回 false，由调用方关闭尚未发布的 conn。
func (server *Server) setUDPConn(conn *net.UDPConn) bool {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	if server.state != serverStarting {
		return false
	}
	server.udpConn = conn
	server.state = serverRunning
	return true
}

// initiateStop 原子地停止新会话准入，并推进服务状态到停止中。
//
// graceful 为 false 时会覆盖先前的优雅停机请求。监听资源先在锁内撤下，再在锁外关闭或取消。
func (server *Server) initiateStop(graceful bool, reason string) {
	server.lifecycleMu.Lock()
	if server.state == serverStopped {
		server.lifecycleMu.Unlock()
		return
	}
	if server.state == serverNew {
		server.state = serverStopped
		server.stopRequested = true
		server.gracefulStop = graceful
		server.stopReason = reason
		forceCancel := server.forceCancel
		server.lifecycleMu.Unlock()
		if !graceful && forceCancel != nil {
			forceCancel()
		}
		server.finishCleanup()
		return
	}
	if !server.stopRequested {
		server.stopRequested = true
		server.gracefulStop = graceful
		server.stopReason = reason
	} else if !graceful {
		server.gracefulStop = false
		server.stopReason = reason
	}
	server.state = serverStopping
	listener := server.tcpListener
	udpConn := server.udpConn
	server.tcpListener = nil
	server.udpConn = nil
	cancel := server.cancel
	server.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if !graceful && server.forceCancel != nil {
		server.forceCancel()
	}
	if listener != nil {
		_ = listener.Close()
	}
	if udpConn != nil {
		_ = udpConn.Close()
	}
}

// isStopRequested 报告是否已由调用方发起受控停止。
func (server *Server) isStopRequested() bool {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	return server.stopRequested
}

// markServeDone 发布监听循环已经退出；完整清理完成由 drained 单独表示。
func (server *Server) markServeDone() {
	server.serveDoneOnce.Do(func() {
		close(server.serveDone)
	})
}

// startCleanup 只启动一个清理 goroutine，使清理流程具有唯一所有者。
func (server *Server) startCleanup() {
	server.cleanupOnce.Do(func() {
		go server.cleanup()
	})
}

// cleanup 按停机模式关闭会话，等待服务端拥有的 goroutine，并最终发布停止状态。
// action 自行派生的业务 goroutine 不归 Server 管理，调用方仍需自行取消和等待。
func (server *Server) cleanup() {
	server.lifecycleMu.Lock()
	graceful := server.gracefulStop
	reason := server.stopReason
	server.lifecycleMu.Unlock()
	if reason == "" {
		reason = "server stopped"
	}

	if graceful {
		ctx, cancel := context.WithTimeout(server.forceCtx, server.config.shutdownWriteTimeout)
		server.closeAllSessions(ctx, reason, true)
		cancel()
	} else {
		server.closeAllSessions(server.forceCtx, reason, false)
	}
	server.handlerWG.Wait()
	server.workerWG.Wait()
	server.finishCleanup()
}

// finishCleanup 幂等清除运行资源并发布完整排空状态。
func (server *Server) finishCleanup() {
	server.finishOnce.Do(func() {
		server.lifecycleMu.Lock()
		server.state = serverStopped
		server.tcpListener = nil
		server.udpConn = nil
		server.lifecycleMu.Unlock()
		server.sessions.clear()
		server.markServeDone()
		close(server.drained)
	})
	if server.forceCancel != nil {
		server.forceCancel()
	}
}

// closeAllSessions 并发关闭会话快照，避免单个慢对端串行拖延整个优雅停机。
// ctx 到期时会中断快照中的传输，但仍等待已启动的关闭任务完成，确保它们纳入 drained。
func (server *Server) closeAllSessions(ctx context.Context, reason string, sendEOF bool) {
	sessions := server.allSessions()
	completed := make(chan struct{}, len(sessions))
	for _, session := range sessions {
		go func(session *AppSession) {
			defer func() { completed <- struct{}{} }()
			var eof []byte
			if sendEOF {
				eof = server.ioEOF
			}
			_ = session.closeWithEOF(ctx, reason, eof)
		}(session)
	}
	for remaining := len(sessions); remaining > 0; {
		select {
		case <-completed:
			remaining--
		case <-ctx.Done():
			for _, session := range sessions {
				session.interrupt()
			}
			for ; remaining > 0; remaining-- {
				<-completed
			}
		}
	}
}

// interruptAllSessions 只取消会话 context 并中断独占传输，不执行用户过滤器或关闭回调。
func (server *Server) interruptAllSessions() {
	for _, session := range server.allSessions() {
		session.interrupt()
	}
}

// configure 仅在 serverNew 状态下执行一次配置变更。
// lifecycleMu 同时隔离 Serve 启动，确保业务配置在运行期保持只读。
func (server *Server) configure(update func() error) error {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	if server.state != serverNew {
		return ErrServerRunning
	}
	return update()
}

// SetEOF 设置优雅停机时尽力发送的协议结束标记。
//
// 数据会复制到 Server 自有内存，并在发送前经过发送过滤器；普通 Close 和 AppSession.Close 不发送该标记。
func (server *Server) SetEOF(eof []byte) error {
	return server.configure(func() error {
		server.ioEOF = append(server.ioEOF[:0], eof...)
		return nil
	})
}

// SetSplitFunc 设置 TCP 字节流和单个 UDP 数据报使用的拆包函数。
// splitFunc 不能为 nil；UDP 不会把未完成帧跨数据报拼接。
func (server *Server) SetSplitFunc(splitFunc bufio.SplitFunc) error {
	if splitFunc == nil {
		return fmt.Errorf("%w: split function must not be nil", ErrInvalidConfig)
	}
	return server.configure(func() error {
		server.splitFunc = splitFunc
		return nil
	})
}

// SetReceiveFilter 同时设置拆包函数和 action 路径解析函数。
// receiveFilter 及其返回的两个函数都不能为 nil；仅可在 Serve 前调用。
func (server *Server) SetReceiveFilter(receiveFilter filter.ReceiveFilter) error {
	if isNilInterface(receiveFilter) {
		return fmt.Errorf("%w: receive filter must not be nil", ErrInvalidConfig)
	}
	return server.configure(func() error {
		server.splitFunc = receiveFilter.SplitFunc()
		server.resolveAction = receiveFilter.ResolveAction()
		if server.splitFunc == nil || server.resolveAction == nil {
			return fmt.Errorf("%w: receive filter returned nil function", ErrInvalidConfig)
		}
		return nil
	})
}

// SetOnMessage 注册不使用命令路由时的默认 action。
// action 返回非 nil 数据时服务端同步回包，返回 nil 时不发送响应。
func (server *Server) SetOnMessage(action ActionFunc) error {
	if action == nil {
		return fmt.Errorf("%w: message action must not be nil", ErrInvalidConfig)
	}
	return server.configure(func() error {
		server.actions[""] = []ActionFunc{action}
		return nil
	})
}

// SetOnError 设置运行错误回调；传入 nil 可清除回调。
// 回调 panic 会被隔离并记录，回调内触发停机应使用 RequestShutdown 或 RequestClose。
func (server *Server) SetOnError(callback func(error)) error {
	return server.configure(func() error {
		server.onError = callback
		return nil
	})
}

// SetOnNewSessionRegister 设置会话成功准入后的同步回调；传入 nil 可清除回调。
// 回调 panic 会被隔离并转交 OnError，回调内不能调用会等待自身的 Shutdown 或 Close。
func (server *Server) SetOnNewSessionRegister(callback func(*AppSession)) error {
	return server.configure(func() error {
		server.onNewSessionRegister = callback
		return nil
	})
}

// SetOnSessionClosed 设置会话完成关闭后的同步回调；传入 nil 可清除回调。
// 回调只触发一次，reason 是关闭发起方提供的诊断文本，不应作为稳定的机器协议使用。
func (server *Server) SetOnSessionClosed(callback func(*AppSession, string)) error {
	return server.configure(func() error {
		server.onSessionClosed = callback
		return nil
	})
}

// RegisterConnectionFilterTCP 替换全部 TCP 连接准入过滤器。
// 过滤器按注册顺序、在 TLS 握手成功后及 AppSession 创建前执行；返回错误会拒绝连接。
func (server *Server) RegisterConnectionFilterTCP(filters ...filter.ConnectionFilterTCP) error {
	return server.configure(func() error {
		server.connectionFilterTCP = append([]filter.ConnectionFilterTCP(nil), filters...)
		return nil
	})
}

// RegisterConnectionFilterUDP 替换全部 UDP 远端地址准入过滤器。
// 每个新 endpoint 创建 AppSession 前按顺序执行；返回错误会丢弃该数据报且不建立会话。
func (server *Server) RegisterConnectionFilterUDP(filters ...filter.ConnectionFilterUDP) error {
	return server.configure(func() error {
		server.connectionFilterUDP = append([]filter.ConnectionFilterUDP(nil), filters...)
		return nil
	})
}

// RegisterSendPacketFilter 替换全部出站数据过滤器。
// 中间件切片会被复制并校验 nil 元素；过滤器同时作用于自动响应、Send 和优雅停机 EOF。
func (server *Server) RegisterSendPacketFilter(middlewares Middlewares) error {
	cloned, err := cloneMiddlewares(middlewares)
	if err != nil {
		return err
	}
	return server.configure(func() error {
		server.sendPacketFilter = cloned
		return nil
	})
}

// RegisterBeforeMiddlewares 替换 action 前执行的全部全局中间件。
// 切片会被复制，调用方后续修改原切片不会改变 Server 行为。
func (server *Server) RegisterBeforeMiddlewares(middlewares Middlewares) error {
	cloned, err := cloneMiddlewares(middlewares)
	if err != nil {
		return err
	}
	return server.configure(func() error {
		server.middlewaresBefore = cloned
		return nil
	})
}

// RegisterAfterMiddlewares 替换 action 后执行的全部全局中间件。
// 切片会被复制，任一中间件返回错误都会终止后续处理和自动回包。
func (server *Server) RegisterAfterMiddlewares(middlewares Middlewares) error {
	cloned, err := cloneMiddlewares(middlewares)
	if err != nil {
		return err
	}
	return server.configure(func() error {
		server.middlewaresAfter = cloned
		return nil
	})
}

// GetSessionByID 根据 ID 返回当前活跃会话；不存在时返回错误。
func (server *Server) GetSessionByID(id string) (*AppSession, error) {
	session, ok := server.sessions.get(id)
	if !ok || session.Closed() {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

// Sessions 返回当前活跃会话的稳定切片快照。
// 返回切片不持有内部锁，调用方可以安全遍历；AppSession 本身仍可能并发关闭。
func (server *Server) Sessions() []*AppSession {
	return server.sessions.sessions()
}

// allSessions 返回包含 closing 会话的内部快照，只用于中断传输和最终清理。
func (server *Server) allSessions() []*AppSession {
	return server.sessions.all()
}

// FindSessions 返回属性快照满足 cond 的活跃会话切片。
// cond 为 nil 时返回 nil；每次调用 cond 都使用独立 map，不会暴露会话内部属性存储。
func (server *Server) FindSessions(cond ConditionFunc) []*AppSession {
	return server.sessions.find(cond)
}

// CountSessions 返回当前活跃会话数量。
func (server *Server) CountSessions() int {
	return server.sessions.len()
}

// bindSession 为新会话安装统一的移除、统计及关闭回调逻辑。
func (server *Server) bindSession(session *AppSession) {
	session.closeTrigger = func(reason string) {
		server.sessions.delete(session)
		server.stats.closedSessions.Add(1)
		server.safeSessionClosed(session, reason)
	}
}

// admitSession 在生命周期锁保护下准入会话，避免停机与会话注册之间的竞态。
// 对相同 ID 的活跃 UDP 会话会返回已有实例，并把 created 置为 false。
func (server *Server) admitSession(session *AppSession) (*AppSession, bool) {
	server.lifecycleMu.Lock()
	defer server.lifecycleMu.Unlock()
	if server.state != serverRunning {
		return nil, false
	}
	server.bindSession(session)
	stored, created := server.sessions.store(session)
	if !created {
		session.closeTrigger = nil
	}
	return stored, created
}

// safeNewSession 调用新会话回调，并把用户 panic 转换为运行错误，避免终止服务循环。
func (server *Server) safeNewSession(session *AppSession) {
	if server.onNewSessionRegister == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			server.handleOnError(fmt.Errorf("new-session callback panic: %v", recovered))
		}
	}()
	server.onNewSessionRegister(session)
}

// safeSessionClosed 调用会话关闭回调，并隔离用户代码产生的 panic。
func (server *Server) safeSessionClosed(session *AppSession, reason string) {
	if server.onSessionClosed == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			server.handleOnError(fmt.Errorf("session-close callback panic: %v", recovered))
		}
	}()
	server.onSessionClosed(session, reason)
}

// handleOnError 记录并转发运行错误。
// 受控停机引发的预期关闭错误会被忽略；OnError 自身的 panic 只写日志，不会递归调用回调。
func (server *Server) handleOnError(err error) {
	if err == nil || (server.isStopRequested() && isExpectedCloseError(err)) {
		return
	}
	server.config.logger.Error("go-server error", "error", err)
	if server.onError == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			server.config.logger.Error("error callback panic", "panic", recovered)
		}
	}()
	server.onError(err)
}

// isExpectedCloseError 判断错误是否由主动关闭传输资源产生。
func isExpectedCloseError(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) || errors.Is(err, ErrServerClosed)
}

// isNilInterface 同时识别 nil 接口和装入接口的 typed nil，避免配置阶段延迟 panic。
func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflection := reflect.ValueOf(value)
	switch reflection.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflection.IsNil()
	default:
		return false
	}
}

// touchReadDeadline 在启用空闲超时时刷新 TCP 读截止时间，0 配置保持原截止时间不变。
func (server *Server) touchReadDeadline(conn net.Conn) error {
	if server.config.idleTimeout <= 0 {
		return nil
	}
	return conn.SetReadDeadline(time.Now().Add(server.config.idleTimeout))
}
