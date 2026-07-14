package goserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// sessionState 表示会话从开放到关闭完成的单向状态。
type sessionState uint32

const (
	sessionOpen sessionState = iota
	sessionClosing
	sessionClosed
)

// AppSession 表示一条 TCP 连接或一个 UDP 远端 endpoint。
//
// 公共方法支持并发调用；同一会话的所有写入由 writeMu 串行化。TCP 会话独占 conn，
// UDP 会话共享服务端 socket 并以 udpAddr 区分对端。会话关闭是幂等操作。
type AppSession struct {
	// ID 是会话的稳定标识；TCP 使用随机 UUID，UDP 使用规范化后的远端地址。
	ID string

	server  *Server
	network Network
	conn    net.Conn
	udpConn *net.UDPConn
	udpAddr netip.AddrPort

	attr             sessionAttr
	sendPacketFilter Middlewares
	state            atomic.Uint32
	lastSeen         atomic.Int64
	writeMu          sync.Mutex
	writeDeadline    time.Time
	closeStarted     atomic.Bool
	activeCounted    atomic.Bool
	interruptOnce    sync.Once
	finalizeOnce     sync.Once
	ctx              context.Context
	cancel           context.CancelFunc
	inactiveTrigger  func()
	closeTrigger     func(string)
}

// sessionAttr 使用读写锁保护可变的用户属性 map。
type sessionAttr struct {
	sync.RWMutex
	values map[string]any
}

// newTCPSession 创建生命周期派生自 Server 的 TCP 会话。
// Server context 取消时，会话 context 同步取消；底层连接在会话最终关闭时释放。
func newTCPSession(server *Server, id string, conn net.Conn) *AppSession {
	ctx, cancel := context.WithCancel(server.ctx)
	return &AppSession{
		ID:               id,
		server:           server,
		network:          TCP,
		conn:             conn,
		attr:             sessionAttr{values: make(map[string]any)},
		sendPacketFilter: server.sendPacketFilter,
		ctx:              ctx,
		cancel:           cancel,
	}
}

// newUDPSession 为指定远端地址创建共享服务端 socket 的 UDP 会话，并记录初始活跃时间。
func newUDPSession(server *Server, id string, conn *net.UDPConn, address netip.AddrPort) *AppSession {
	ctx, cancel := context.WithCancel(server.ctx)
	session := &AppSession{
		ID:               id,
		server:           server,
		network:          UDP,
		udpConn:          conn,
		udpAddr:          address,
		attr:             sessionAttr{values: make(map[string]any)},
		sendPacketFilter: server.sendPacketFilter,
		ctx:              ctx,
		cancel:           cancel,
	}
	session.touch()
	return session
}

// Context 返回会话级 context；会话传输被中断或所属 Server 开始停止时该 context 会被取消。
// 调用方不应保存并调用其内部 cancel，也不应假设取消后 action 派生的 goroutine 会被自动等待。
func (session *AppSession) Context() context.Context {
	return session.ctx
}

// Network 返回当前会话使用的传输协议。
func (session *AppSession) Network() Network {
	return session.network
}

// RemoteAddr 返回对端地址。
// UDP 每次返回由不可变 AddrPort 构造的地址；TCP 在连接不可用时可能返回 nil。
func (session *AppSession) RemoteAddr() net.Addr {
	if session.network == UDP {
		return net.UDPAddrFromAddrPort(session.udpAddr)
	}
	if session.conn == nil {
		return nil
	}
	return session.conn.RemoteAddr()
}

// Closed 报告会话是否已经开始关闭或传输已被中断。
// 返回 true 后 Send 和 SendRaw 均返回 ErrSessionClosed；关闭回调可能仍在排空。
func (session *AppSession) Closed() bool {
	return sessionState(session.state.Load()) != sessionOpen
}

// Send 依次应用发送过滤器，并把完整结果写入对端。
//
// 同一会话的并发写入会串行化；ctx 为 nil 时使用 context.Background。
// TCP 保证写完全部字节或返回错误，UDP 保证一次 Write 对应一个完整数据报。
func (session *AppSession) Send(ctx context.Context, data []byte) error {
	return session.send(ctx, data, true)
}

// sendResponse 写入同步 action 响应，并复用会话 context 的传输关闭语义以避免额外取消 watcher 分配。
func (session *AppSession) sendResponse(ctx context.Context, data []byte) error {
	return session.send(ctx, data, false)
}

// send 在持有写锁期间执行发送过滤器和传输写入，并按需监听任意调用方 context 的取消。
func (session *AppSession) send(ctx context.Context, data []byte, watchCancel bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if sessionState(session.state.Load()) != sessionOpen {
		return ErrSessionClosed
	}
	filtered, err := session.applySendFilters(ctx, data)
	if err != nil {
		return err
	}
	return session.writeLocked(ctx, filtered, watchCancel)
}

// SendRaw 跳过发送过滤器并直接写入完整数据。
// 其串行化、context、TCP 完整写入和 UDP 数据报语义与 Send 相同。
func (session *AppSession) SendRaw(ctx context.Context, data []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if sessionState(session.state.Load()) != sessionOpen {
		return ErrSessionClosed
	}
	return session.writeLocked(ctx, data, true)
}

// applySendFilters 在调用方持有 writeMu 时按注册顺序转换出站数据。
// 任一过滤器返回错误都会终止后续过滤器和网络写入。
func (session *AppSession) applySendFilters(ctx context.Context, data []byte) ([]byte, error) {
	var err error
	for _, middleware := range session.sendPacketFilter {
		data, err = middleware(ctx, session, data)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

// writeLocked 在调用方已持有 writeMu 的前提下写入 TCP 字节流或单个 UDP 数据报。
func (session *AppSession) writeLocked(ctx context.Context, data []byte, watchCancel bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session.network == UDP {
		n, err := session.udpConn.WriteToUDPAddrPort(data, session.udpAddr)
		if n > 0 {
			session.server.stats.bytesWritten.Add(uint64(n))
		}
		if err != nil {
			return err
		}
		if n != len(data) {
			return io.ErrShortWrite
		}
		return nil
	}
	if !watchCancel {
		return session.writeTCPFast(ctx, data)
	}
	resetDeadline, err := setWriteDeadline(ctx, session.conn, session.server.config.writeTimeout, watchCancel)
	if err != nil {
		return err
	}
	err = session.writeTCPData(ctx, data)
	resetDeadline()
	session.writeDeadline = time.Time{}
	return err
}

// writeTCPFast 为同步 action 自动响应复用 TCP 写截止时间，避免每次回包创建取消 watcher。
// 若 ctx 自带截止时间则直接采用；否则仅在旧截止时间即将过期时刷新默认超时。
func (session *AppSession) writeTCPFast(ctx context.Context, data []byte) error {
	if contextDeadline, ok := ctx.Deadline(); ok {
		if err := session.conn.SetWriteDeadline(contextDeadline); err != nil {
			return err
		}
		session.writeDeadline = contextDeadline
	} else if timeout := session.server.config.writeTimeout; timeout > 0 {
		now := time.Now()
		if session.writeDeadline.IsZero() || session.writeDeadline.Sub(now) < timeout/2 {
			session.writeDeadline = now.Add(timeout)
			if err := session.conn.SetWriteDeadline(session.writeDeadline); err != nil {
				return err
			}
		}
	}
	return session.writeTCPData(ctx, data)
}

// writeTCPData 循环写完全部 TCP 数据并累计实际写入字节数，本函数不修改连接截止时间。
func (session *AppSession) writeTCPData(ctx context.Context, data []byte) error {
	for len(data) > 0 {
		n, writeErr := session.conn.Write(data)
		if n > 0 {
			session.server.stats.bytesWritten.Add(uint64(n))
			data = data[n:]
		}
		if writeErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return writeErr
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

// setWriteDeadline 取 context 截止时间与默认超时中的较早值设置 TCP 写截止时间。
//
// watchCancel 为 true 时还通过 context.AfterFunc 在无 deadline 的主动取消发生后立即唤醒阻塞写；
// 返回的清理函数必须调用，它会等待已触发回调结束并清除连接截止时间，避免影响后续写入。
func setWriteDeadline(ctx context.Context, conn net.Conn, fallback time.Duration, watchCancel bool) (func(), error) {
	var deadline time.Time
	if fallback > 0 {
		deadline = time.Now().Add(fallback)
	}
	if contextDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || contextDeadline.Before(deadline)) {
		deadline = contextDeadline
	}
	if !deadline.IsZero() {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return nil, err
		}
	}
	stopCancel := func() bool { return true }
	var cancelCallbackDone <-chan struct{}
	if watchCancel && ctx.Done() != nil {
		done := make(chan struct{})
		cancelCallbackDone = done
		stopCancel = context.AfterFunc(ctx, func() {
			defer close(done)
			_ = conn.SetWriteDeadline(time.Now())
		})
	}
	return func() {
		if !stopCancel() && cancelCallbackDone != nil {
			<-cancelCallbackDone
		}
		_ = conn.SetWriteDeadline(time.Time{})
	}, nil
}

// Close 立即关闭会话且不发送 Server 配置的 EOF 标记。
// reason 会传给 OnSessionClosed 作为诊断信息；重复调用是幂等的并返回 nil。
func (session *AppSession) Close(reason string) error {
	if !session.beginClose() {
		return nil
	}
	session.interrupt()
	session.finalizeClose(reason)
	return nil
}

// closeWithEOF 在最终关闭前，受 ctx 约束地尽力发送 EOF 标记。
// EOF 会经过发送过滤器；无论发送是否成功都会完成关闭，并把发送错误返回给调用方。
func (session *AppSession) closeWithEOF(ctx context.Context, reason string, eof []byte) error {
	if !session.beginClose() {
		session.finalizeClose(reason)
		return nil
	}
	var sendErr error
	if len(eof) > 0 {
		session.writeMu.Lock()
		filtered, err := session.applySendFilters(ctx, eof)
		if err == nil {
			err = session.writeLocked(ctx, filtered, true)
		}
		sendErr = err
		session.writeMu.Unlock()
	}
	session.finalizeClose(reason)
	return sendErr
}

// forceClose 关闭底层传输以解除正在进行的网络操作，并确保最终清理只执行一次。
func (session *AppSession) forceClose(reason string) {
	session.beginClose()
	session.interrupt()
	session.finalizeClose(reason)
}

// beginClose 把会话推进到 closing，并让一个调用方取得主动关闭流程的所有权。
// interrupt 只推进状态而不取得所有权，因此后续 cleanup 仍能完成最终关闭。
func (session *AppSession) beginClose() bool {
	session.markClosing()
	return session.closeStarted.CompareAndSwap(false, true)
}

// markClosing 首次把开放会话推进到 closing，并同步从公开活跃会话集合下线。
func (session *AppSession) markClosing() bool {
	if !session.state.CompareAndSwap(uint32(sessionOpen), uint32(sessionClosing)) {
		return false
	}
	if session.inactiveTrigger != nil {
		session.inactiveTrigger()
	}
	return true
}

// interrupt 取消会话 context 并关闭独占 TCP 连接，不触发用户关闭回调。
// UDP socket 由 Server 共享，服务停止时由监听循环统一关闭。
func (session *AppSession) interrupt() {
	session.markClosing()
	session.interruptOnce.Do(func() {
		if session.cancel != nil {
			session.cancel()
		}
		if session.network == TCP && session.conn != nil {
			_ = session.conn.Close()
		}
	})
}

// finalizeClose 完成状态转换，并且只触发一次关闭回调。
func (session *AppSession) finalizeClose(reason string) {
	session.finalizeOnce.Do(func() {
		session.interrupt()
		session.state.Store(uint32(sessionClosed))
		if session.closeTrigger != nil {
			session.closeTrigger(reason)
		}
	})
}

// AddAttr 仅在 key 尚不存在时新增并发安全的会话属性。
// key 已存在时返回错误；value 若为 map、slice 等引用类型，其内部并发安全仍由调用方负责。
func (session *AppSession) AddAttr(key string, value any) error {
	session.attr.Lock()
	defer session.attr.Unlock()
	if session.attr.values == nil {
		session.attr.values = make(map[string]any)
	}
	if _, exists := session.attr.values[key]; exists {
		return errors.New("attribute already exists")
	}
	session.attr.values[key] = value
	return nil
}

// SetAttr 新增或替换一个并发安全的会话属性。
func (session *AppSession) SetAttr(key string, value any) {
	session.attr.Lock()
	defer session.attr.Unlock()
	if session.attr.values == nil {
		session.attr.values = make(map[string]any)
	}
	session.attr.values[key] = value
}

// GetAttr 返回指定会话属性；key 不存在时返回错误。
func (session *AppSession) GetAttr(key string) (any, error) {
	session.attr.RLock()
	defer session.attr.RUnlock()
	value, exists := session.attr.values[key]
	if !exists {
		return nil, errors.New("attribute does not exist")
	}
	return value, nil
}

// DelAttr 删除指定会话属性；key 不存在时返回错误。
func (session *AppSession) DelAttr(key string) error {
	session.attr.Lock()
	defer session.attr.Unlock()
	if _, exists := session.attr.values[key]; !exists {
		return errors.New("attribute does not exist")
	}
	delete(session.attr.values, key)
	return nil
}

// attrSnapshot 复制属性 map，使会话筛选回调不能直接修改内部存储。
// 这是浅拷贝，属性值所引用对象的所有权不发生变化。
func (session *AppSession) attrSnapshot() map[string]any {
	session.attr.RLock()
	defer session.attr.RUnlock()
	result := make(map[string]any, len(session.attr.values))
	for key, value := range session.attr.values {
		result[key] = value
	}
	return result
}

// touch 原子记录最近一次 UDP 入站活动时间，供集中式空闲扫描器使用。
func (session *AppSession) touch() {
	session.lastSeen.Store(time.Now().UnixNano())
}

// idleFor 返回 UDP endpoint 到 now 为止的无入站活动时长；尚未记录活动时返回 0。
func (session *AppSession) idleFor(now time.Time) time.Duration {
	lastSeen := session.lastSeen.Load()
	if lastSeen == 0 {
		return 0
	}
	return now.Sub(time.Unix(0, lastSeen))
}

// formatSessionError 为操作错误补充会话 ID 和阶段信息，同时保留 errors.Is/As 错误链。
func formatSessionError(session *AppSession, operation string, err error) error {
	return fmt.Errorf("session %s %s: %w", session.ID, operation, err)
}
