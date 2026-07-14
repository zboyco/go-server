package goserver

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
)

// udpReadBufferSize 为 socket 读取保留完整 UDP 数据报边界所需的固定缓冲容量。
const udpReadBufferSize = 64 * 1024

// udpPacket 持有一个待读取 worker 处理的数据报及其缓冲区所有权。
// queued 模式下，buffer 在包进入队列后只能由对应 worker 读取并最终归还缓冲池。
type udpPacket struct {
	buffer []byte
	size   int
	addr   netip.AddrPort
}

// udpInlineRequest 把单个数据报及其处理完成信号交给 inline worker。
type udpInlineRequest struct {
	packet udpPacket
}

// udpRuntimeError 描述由 UDP 读取循环产生、需要在独立 worker 中报告的错误。
type udpRuntimeError struct {
	err          error
	callbackOnly bool
}

// datagramBufferPool 在 UDP 读取之间复用固定为 64 KiB 的完整数据报缓冲区。
type datagramBufferPool struct {
	pool sync.Pool
}

// newDatagramBufferPool 创建一个按需分配 64 KiB 切片的 UDP 接收缓冲池。
func newDatagramBufferPool() *datagramBufferPool {
	pool := &datagramBufferPool{}
	pool.pool.New = func() any {
		return make([]byte, udpReadBufferSize)
	}
	return pool
}

// get 从池中取得长度为 udpReadBufferSize 的可写接收缓冲区。
func (pool *datagramBufferPool) get() []byte {
	return pool.pool.Get().([]byte)[:udpReadBufferSize]
}

// put 仅回收容量严格等于 udpReadBufferSize 的缓冲区，避免异常大切片长期驻留。
func (pool *datagramBufferPool) put(buffer []byte) {
	if cap(buffer) != udpReadBufferSize {
		return
	}
	pool.pool.Put(buffer[:udpReadBufferSize])
}

// udpRuntime 保存 queued 模式下固定数量的有界分片队列及共享缓冲池。
type udpRuntime struct {
	queues []chan udpPacket
	pool   *datagramBufferPool
}

// newUDPRuntime 根据已校验的 worker 数和队列容量分配固定 UDP 运行结构。
func newUDPRuntime(workers, queueSize int) *udpRuntime {
	runtime := &udpRuntime{
		queues: make([]chan udpPacket, workers),
		pool:   newDatagramBufferPool(),
	}
	for index := range runtime.queues {
		runtime.queues[index] = make(chan udpPacket, queueSize)
	}
	return runtime
}

// serveUDP 解析并绑定 UDP 地址，然后按配置启动 queued 或 inline 调度路径。
// 错误回调 worker、可选空闲扫描器及数据报 worker 均受 workerWG 跟踪。
func (server *Server) serveUDP() error {
	address, err := net.ResolveUDPAddr("udp", server.config.address)
	if err != nil {
		return fmt.Errorf("resolve UDP address %s: %w", server.config.address, err)
	}
	conn, err := net.ListenUDP("udp", address)
	if err != nil {
		return fmt.Errorf("listen UDP %s: %w", server.config.address, err)
	}
	if !server.setUDPConn(conn) {
		_ = conn.Close()
		return ErrServerClosed
	}
	defer conn.Close()
	server.startRouteLogging()
	errorQueue := make(chan udpRuntimeError, server.config.udpQueueSize)
	server.workerWG.Add(1)
	go server.udpErrorWorker(errorQueue)
	defer close(errorQueue)
	if server.config.idleTimeout > 0 {
		server.workerWG.Add(1)
		go server.udpIdleSweeper()
	}
	if server.config.udpDispatchMode == UDPDispatchInline {
		return server.serveUDPInline(conn, errorQueue)
	}
	return server.serveUDPQueued(conn, errorQueue)
}

// serveUDPQueued 持续读取数据报，并按规范化远端地址分发到有序、有界的 worker 分片。
//
// 成功入队后缓冲区所有权转交 worker；队列满或数据报超限时由读取循环立即归还缓冲区。
// 队列满属于预期高频过载信号，只通知 OnError 而不逐包写日志。
func (server *Server) serveUDPQueued(conn *net.UDPConn, errorQueue chan<- udpRuntimeError) error {
	runtime := newUDPRuntime(server.config.udpWorkers, server.config.udpQueueSize)
	server.lifecycleMu.Lock()
	server.udpRuntime = runtime
	server.lifecycleMu.Unlock()
	for index := range runtime.queues {
		server.workerWG.Add(1)
		go server.udpWorker(runtime.queues[index], runtime.pool)
	}

	for {
		buffer := runtime.pool.get()
		size, remoteAddr, readErr := conn.ReadFromUDPAddrPort(buffer)
		if readErr != nil {
			runtime.pool.put(buffer)
			return fmt.Errorf("read UDP: %w", readErr)
		}
		remoteAddr = netip.AddrPortFrom(remoteAddr.Addr().Unmap(), remoteAddr.Port())
		server.stats.packetsRead.Add(1)
		server.stats.bytesRead.Add(uint64(size))
		if size > server.config.maxDatagramSize {
			server.stats.packetsDropped.Add(1)
			runtime.pool.put(buffer)
			if !server.enqueueUDPError(errorQueue, udpRuntimeError{err: fmt.Errorf("%w: got %d bytes, max %d", ErrDatagramTooLarge, size, server.config.maxDatagramSize)}) {
				return server.ctx.Err()
			}
			continue
		}

		packet := udpPacket{buffer: buffer, size: size, addr: remoteAddr}
		queue := runtime.queues[udpShard(remoteAddr, len(runtime.queues))]
		select {
		case queue <- packet:
		default:
			server.stats.packetsDropped.Add(1)
			runtime.pool.put(buffer)
			server.tryEnqueueUDPError(errorQueue, udpRuntimeError{err: ErrUDPQueueFull, callbackOnly: true})
		}
	}
}

// serveUDPInline 通过单 worker 严格串行处理数据报，使 socket 读取循环不执行用户代码。
// 每次处理完成前不会读取下一包；停止时读循环可以放弃等待，仍由 worker 保持缓冲区存活。
func (server *Server) serveUDPInline(conn *net.UDPConn, errorQueue chan<- udpRuntimeError) error {
	requests := make(chan udpInlineRequest)
	completed := make(chan struct{}, 1)
	server.workerWG.Add(1)
	go server.udpInlineWorker(requests, completed)
	defer close(requests)

	buffer := make([]byte, udpReadBufferSize)
	for {
		size, remoteAddr, readErr := conn.ReadFromUDPAddrPort(buffer)
		if readErr != nil {
			return fmt.Errorf("read UDP: %w", readErr)
		}
		remoteAddr = netip.AddrPortFrom(remoteAddr.Addr().Unmap(), remoteAddr.Port())
		server.stats.packetsRead.Add(1)
		server.stats.bytesRead.Add(uint64(size))
		if size > server.config.maxDatagramSize {
			server.stats.packetsDropped.Add(1)
			if !server.enqueueUDPError(errorQueue, udpRuntimeError{err: fmt.Errorf("%w: got %d bytes, max %d", ErrDatagramTooLarge, size, server.config.maxDatagramSize)}) {
				return server.ctx.Err()
			}
			continue
		}
		request := udpInlineRequest{
			packet: udpPacket{buffer: buffer, size: size, addr: remoteAddr},
		}
		select {
		case requests <- request:
		case <-server.ctx.Done():
			return server.ctx.Err()
		}
		select {
		case <-completed:
		case <-server.ctx.Done():
			return server.ctx.Err()
		}
	}
}

// udpInlineWorker 在独立 goroutine 中执行 inline action，并在每个数据报完成后确认所有权归还。
func (server *Server) udpInlineWorker(requests <-chan udpInlineRequest, completed chan<- struct{}) {
	defer server.workerWG.Done()
	for request := range requests {
		server.handleUDPPacket(request.packet)
		completed <- struct{}{}
	}
}

// udpErrorWorker 串行执行 UDP 读循环产生的错误回调，使用户代码不能阻塞 Serve。
func (server *Server) udpErrorWorker(errors <-chan udpRuntimeError) {
	defer server.workerWG.Done()
	for runtimeError := range errors {
		if runtimeError.callbackOnly {
			server.notifyErrorCallback(runtimeError.err)
			continue
		}
		server.handleOnError(runtimeError.err)
	}
}

// enqueueUDPError 在正常运行时保留错误事件；停止后允许读循环放弃投递并退出。
func (server *Server) enqueueUDPError(queue chan<- udpRuntimeError, runtimeError udpRuntimeError) bool {
	select {
	case queue <- runtimeError:
		return true
	case <-server.ctx.Done():
		return false
	}
}

// tryEnqueueUDPError 对高频过载事件执行有界投递，避免错误回调反向放大队列压力。
func (server *Server) tryEnqueueUDPError(queue chan<- udpRuntimeError, runtimeError udpRuntimeError) {
	select {
	case queue <- runtimeError:
	default:
	}
}

// udpWorker 串行处理一个分片队列，保持映射到该分片的 endpoint 数据报顺序。
//
// 服务 context 取消后不再执行 action，并排空队列、归还所有已转交的缓冲区后退出。
func (server *Server) udpWorker(queue <-chan udpPacket, pool *datagramBufferPool) {
	defer server.workerWG.Done()
	for {
		select {
		case packet := <-queue:
			if server.ctx.Err() != nil {
				pool.put(packet.buffer)
				for {
					select {
					case packet = <-queue:
						pool.put(packet.buffer)
					default:
						return
					}
				}
			}
			server.handleUDPPacket(packet)
			pool.put(packet.buffer)
		case <-server.ctx.Done():
			for {
				select {
				case packet := <-queue:
					pool.put(packet.buffer)
				default:
					return
				}
			}
		}
	}
}

// handleUDPPacket 为远端 endpoint 创建或复用会话，并分发一个完整 UDP 数据报。
//
// endpoint 使用 IPv4-mapped IPv6 规范化后的 AddrPort 作为会话 ID。新会话只有通过全部连接过滤器且
// 在运行态成功准入后才触发回调；协议错误会关闭该 endpoint 会话，但不会终止整个 UDP 服务。
func (server *Server) handleUDPPacket(packet udpPacket) {
	if server.ctx.Err() != nil {
		return
	}
	var session *AppSession
	defer func() {
		if recovered := recover(); recovered != nil {
			if session != nil {
				_ = session.Close("UDP handler panic")
			}
			server.handleOnError(fmt.Errorf("UDP handler panic for %s: %v", packet.addr, recovered))
		}
	}()

	sessionID := packet.addr.String()
	session, exists := server.sessions.get(sessionID)
	if !exists || session.Closed() {
		udpAddress := net.UDPAddrFromAddrPort(packet.addr)
		for _, connectionFilter := range server.connectionFilterUDP {
			if connectionFilter == nil {
				continue
			}
			if err := connectionFilter(udpAddress); err != nil {
				return
			}
		}
		candidate := newUDPSession(server, sessionID, server.udpConn, packet.addr)
		stored, created := server.admitSession(candidate)
		if stored == nil {
			candidate.forceClose("session admission rejected")
			return
		}
		session = stored
		if created {
			server.stats.acceptedConnections.Add(1)
			server.safeNewSession(session)
		} else {
			candidate.forceClose("duplicate UDP session")
		}
	}
	if session.Closed() || session.Context().Err() != nil {
		return
	}
	session.touch()
	if err := server.decodeUDPDatagram(session, packet.buffer[:packet.size]); err != nil {
		server.stats.protocolErrors.Add(1)
		server.handleOnError(formatSessionError(session, "decode UDP datagram", err))
		_ = session.Close("UDP protocol error")
	}
}

// decodeUDPDatagram 只在当前 UDP 数据报内部拆分并分发完整帧，绝不把剩余字节带到下一数据报。
//
// splitFunc 必须在 atEOF=true 时消费数据或返回 bufio.ErrFinalToken；advance 非法、零推进的
// 不完整帧及解析错误统一返回协议错误。ErrFinalToken 携带的 token 会在正常结束前分发一次。
func (server *Server) decodeUDPDatagram(session *AppSession, data []byte) error {
	if len(data) == 0 {
		return server.dispatchUDPToken(session, []byte{})
	}
	for len(data) > 0 {
		advance, token, err := server.splitFunc(data, true)
		finalToken := err != nil && errors.Is(err, bufio.ErrFinalToken)
		if err != nil && !finalToken {
			return err
		}
		if advance < 0 || advance > len(data) {
			return fmt.Errorf("%w: split advance %d for %d bytes", ErrInvalidFrame, advance, len(data))
		}
		if advance == 0 && !finalToken {
			return fmt.Errorf("%w: incomplete UDP frame", ErrInvalidFrame)
		}
		if token != nil {
			if err := server.dispatchUDPToken(session, token); err != nil {
				return err
			}
		}
		if finalToken {
			return nil
		}
		data = data[advance:]
	}
	return nil
}

// dispatchUDPToken 解析并分发一个 UDP token；空数据报使用零长度 token 走同一路径。
func (server *Server) dispatchUDPToken(session *AppSession, token []byte) error {
	actionName := ""
	if server.resolveAction != nil {
		var err error
		actionName, token, err = server.resolveAction(token)
		if err != nil {
			return err
		}
	}
	if err := server.hookAction(session.Context(), actionName, session, token); err != nil {
		server.handleOnError(formatSessionError(session, "dispatch UDP action", err))
	}
	return nil
}

// udpIdleSweeper 使用一个服务级 ticker 关闭超过 idleTimeout 未收到入站数据的 UDP 会话。
// 共享扫描器避免为每个 endpoint 创建定时器或后台 goroutine。
func (server *Server) udpIdleSweeper() {
	defer server.workerWG.Done()
	interval := time.Second
	if server.config.idleTimeout < interval {
		interval = server.config.idleTimeout
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			for _, session := range server.Sessions() {
				if session.network == UDP && session.idleFor(now) >= server.config.idleTimeout {
					_ = session.Close("UDP idle timeout")
				}
			}
		case <-server.ctx.Done():
			return
		}
	}
}

// udpShard 使用 FNV-1a 风格散列把 endpoint 映射到固定分片，且不分配地址字符串。
func udpShard(address netip.AddrPort, shardCount int) int {
	bytes := address.Addr().As16()
	var hash uint64 = 1469598103934665603
	for _, value := range bytes {
		hash ^= uint64(value)
		hash *= 1099511628211
	}
	hash ^= uint64(address.Port())
	hash *= 1099511628211
	return int(hash % uint64(shardCount))
}

// notifyErrorCallback 仅通知 OnError，用于队列满等高频条件，避免逐包日志放大过载。
// 用户回调的 panic 会被吞掉，因为此路径不能递归记录更多高频错误。
func (server *Server) notifyErrorCallback(err error) {
	if server.onError == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	server.onError(err)
}
