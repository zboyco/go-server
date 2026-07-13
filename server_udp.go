package goserver

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// New 新建一个tcp服务
func NewUDP(ip string, port int) *Server {
	return newServer(UDP, ip, port, nil)
}

// startUDP 开始监听
func (server *Server) startUDP(addr string) {
	// 解析地址
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		server.handleOnError(errors.Wrap(err, "resolve udp addr error"))
		return
	}

	// 监听UDP连接
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		server.handleOnError(errors.Wrap(err, "listen udp error"))
		return
	}
	if !server.setUDPConn(udpConn) {
		_ = udpConn.Close()
		return
	}

	// 程序返回后关闭socket
	defer udpConn.Close()

	// 开启会话池管理
	go server.sessionSource.sessionPoolManager(server.stopSignal())

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		bufferLength := 4 * 1024
		for {
			// 开始接收udp数据
			buffer := make([]byte, bufferLength)
			n, clientAddr, err := udpConn.ReadFromUDP(buffer)
			if err != nil {
				if server.isStopping() {
					return
				}
				server.handleOnError(errors.Wrap(err, "read udp error"))
				continue
			}
			server.handleUDPClient(udpConn, clientAddr, buffer[:n])
		}
	}()

	server.printServerInfo()

	wg.Wait()
}

// handleUDPClient 读取 UDP 数据
func (server *Server) handleUDPClient(conn net.Conn, clientAddr *net.UDPAddr, data []byte) {
	// 连接过滤器
	if server.connectionFilterUDP != nil {
		for i := range server.connectionFilterUDP {
			if err := server.connectionFilterUDP[i](clientAddr); err != nil {
				slog.Warn(fmt.Sprintf("connect[%s] filter because %s", clientAddr.String(), err.Error()))
				return
			}
		}
	}

	// 计算MD5
	md5Sum := md5.Sum([]byte(clientAddr.String()))
	// 生成会话ID
	sessionID := hex.EncodeToString(md5Sum[:])

	session, _ := server.GetSessionByID(sessionID)
	if session != nil && session.isClosed() {
		session = nil
	}
	created := false
	if session == nil {
		// 创建会话对象
		newSession := &AppSession{
			ID:               sessionID,
			network:          UDP,
			conn:             conn,
			attr:             sessionAttr{values: make(map[string]interface{})},
			sendPacketFilter: server.sendPacketFilter,

			udpAddr:         clientAddr,
			udpReadDeadline: time.Now().Add(server.idleSessionTimeOutDuration),
			udpClientIO:     NewSafeByteSlice(),
		}
		// 设置会话关闭触发器
		newSession.closeTrigger = server.closeSessionTrigger(newSession)

		session, created = server.sessionSource.storeSession(newSession)
		if session == nil {
			return
		}
	}

	if created {
		// 获取连接地址
		slog.Debug(fmt.Sprintf("client[%s] address: %s", session.ID, clientAddr.String()))

		// 新客户端接入通知
		if server.onNewSessionRegister != nil {
			server.onNewSessionRegister(session)
		}

		// 启动超时检测
		go server.udpReadTimeout(session)

		// 启动数据分离
		go server.udpSplitData(session)
	}

	// 更新超时时间
	session.setUDPReadDeadline(time.Now().Add(server.idleSessionTimeOutDuration))
	// 将读取的数据写入 buffer
	_, _ = session.udpClientIO.Write(data)
}

// udpReadTimeout 读取超时
func (server *Server) udpReadTimeout(session *AppSession) {
	if server.IdleSessionTimeOut <= 0 {
		return
	}
	for {
		time.Sleep(time.Second)
		if time.Now().After(session.getUDPReadDeadline()) {
			ip := "127.0.0.1"
			if server.ip != "" {
				ip = server.ip
			}
			server.closeSession(session, fmt.Sprintf("read udp %s:%d->%s: i/o timeout", ip, server.port, session.udpAddr.String()))
			return
		}
	}
}

// udpSplitData 数据拆分
func (server *Server) udpSplitData(session *AppSession) {
	var err error

	// 创建scanner
	scanner := bufio.NewScanner(session.udpClientIO)
	if server.maxScanTokenSize > 0 {
		if server.maxScanTokenSize > 4*1024 {
			scanner.Buffer(make([]byte, 0, 4*1024), server.maxScanTokenSize)
		} else {
			scanner.Buffer(make([]byte, 0, server.maxScanTokenSize), server.maxScanTokenSize)
		}
	}

	// 设置分离函数
	scanner.Split(server.splitFunc)

	// 获取数据
	for scanner.Scan() {
		token := scanner.Bytes()
		actionName := ""
		if server.resolveAction != nil {
			actionName, token, err = server.resolveAction(token)
			if err != nil {
				break
			}
		}
		hookErr := server.hookAction(actionName, session, token)
		if hookErr != nil {
			server.handleOnError(hookErr)
		}
	}

	// 错误处理
	if err == nil {
		err = scanner.Err()
	}
	if err != nil {
		server.handleOnError(errors.Wrap(err, "scan udp error"))
		server.closeSession(session, err.Error())
		return
	}
	if !session.isClosed() {
		server.closeSession(session, "EOF")
	}
}

// SafeByteSlice 实现了阻塞式 io.ReadWriteCloser，并通过互斥锁保证并发安全。
type SafeByteSlice struct {
	buffer bytes.Buffer
	sync.Mutex
	cond   *sync.Cond
	closed bool
}

// NewSafeByteSlice 创建一个 SafeByteSlice 实例
func NewSafeByteSlice() *SafeByteSlice {
	s := &SafeByteSlice{}
	s.cond = sync.NewCond(&s.Mutex)
	return s
}

// Read 实现了 io.Reader 接口
func (s *SafeByteSlice) Read(p []byte) (n int, err error) {
	s.Lock()
	defer s.Unlock()

	s.initCond()
	for s.buffer.Len() == 0 && !s.closed {
		s.cond.Wait()
	}
	if s.buffer.Len() == 0 && s.closed {
		return 0, io.EOF
	}
	return s.buffer.Read(p)
}

// Write 实现了 io.Writer 接口
func (s *SafeByteSlice) Write(p []byte) (n int, err error) {
	s.Lock()
	defer s.Unlock()

	s.initCond()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	n, err = s.buffer.Write(p)
	s.cond.Signal()
	return n, err
}

// Close 关闭缓冲区并唤醒阻塞中的读操作。
func (s *SafeByteSlice) Close() error {
	s.Lock()
	defer s.Unlock()

	s.initCond()
	s.closed = true
	s.cond.Broadcast()
	return nil
}

func (s *SafeByteSlice) initCond() {
	if s.cond == nil {
		s.cond = sync.NewCond(&s.Mutex)
	}
}
