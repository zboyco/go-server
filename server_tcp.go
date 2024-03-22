package goserver

import (
	"bufio"
	"crypto/tls"
	"log"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
)

// New 新建一个tcp服务
func New(network Network, ip string, port int) *Server {
	switch network {
	case TCP:
		return NewTCP(ip, port)
	case UDP:
		return NewUDP(ip, port)
	default:
	}

	return nil
}

func NewTCP(ip string, port int) *Server {
	return NewTCPWithTLS(ip, port, nil)
}

// NewWithTLS 新建一个tls加密服务
func NewTCPWithTLS(ip string, port int, config *tls.Config) *Server {
	return newServer(TCP, ip, port, config)
}

// startTCP 开始监听
func (server *Server) startTCP(addr string) {
	var (
		tcpListener net.Listener
		err         error
	)

	// 监听端口
	if server.tlsConfig == nil {
		tcpListener, err = net.Listen("tcp", addr)
	} else {
		tcpListener, err = tls.Listen("tcp", addr, server.tlsConfig)
	}
	if err != nil {
		log.Println("监听出错, ", err)
		server.handleOnError(err)
		return
	}

	// 程序返回后关闭socket
	defer tcpListener.Close()

	// 开启会话池管理
	go server.sessionSource.sessionPoolManager()

	var wg sync.WaitGroup
	for i := 0; i < server.AcceptCount; i++ {
		wg.Add(1)
		go func(acceptIndex int) {
			defer wg.Done()
			for {
				// 开始接收连接
				conn, err := tcpListener.Accept()
				if err != nil {
					log.Println("客户端连接失败, ", err)
					server.handleOnError(err)
					continue
				}
				// 启用goroutine处理
				go server.handleTCPClient(conn)
			}
		}(i)
	}

	server.printServerInfo()

	wg.Wait()
}

// handleTCPClient 读取数据
func (server *Server) handleTCPClient(conn net.Conn) {
	// 连接过滤器
	if server.connectionFilterTCP != nil {
		for i := range server.connectionFilterTCP {
			if err := server.connectionFilterTCP[i](conn); err != nil {
				log.Printf("connect[%s] filter because %s", conn.RemoteAddr(), err.Error())
				_ = conn.Close()
				return
			}
		}
	}

	// 创建会话对象
	session := &AppSession{
		ID:               uuid.NewString(),
		network:          TCP,
		conn:             conn,
		attr:             make(map[string]interface{}),
		sendPacketFilter: server.sendPacketFilter,
	}

	// 获取连接地址
	remoteAddr := session.conn.RemoteAddr()
	log.Println("客户端[", session.ID, "]地址:", remoteAddr)

	// 新客户端接入通知
	if server.onNewSessionRegister != nil {
		server.onNewSessionRegister(session)
	}

	// 注册Session
	server.sessionSource.addSession(session)

	// 创建scanner
	scanner := bufio.NewScanner(session.conn)
	if server.maxScanTokenSize > 0 {
		if server.maxScanTokenSize > 4*1024 {
			scanner.Buffer(make([]byte, 0, 4*1024), server.maxScanTokenSize)
		} else {
			scanner.Buffer(make([]byte, 0, server.maxScanTokenSize), server.maxScanTokenSize)
		}
	}

	// 设置分离函数
	scanner.Split(server.splitFunc)

	// 设置闲置超时时间
	if server.IdleSessionTimeOut > 0 {
		err := session.conn.SetReadDeadline(time.Now().Add(server.idleSessionTimeOutDuration))
		if err != nil {
			log.Println(err)
		}
	}

	var err error
	// 获取数据
	for scanner.Scan() {
		// 设置闲置超时时间
		if server.IdleSessionTimeOut > 0 {
			err = session.conn.SetReadDeadline(time.Now().Add(server.idleSessionTimeOutDuration))
			if err != nil {
				break
			}
		}
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
		server.handleOnError(err)
		server.closeSession(session, err.Error())
		return
	}
	server.closeSession(session, "EOF")
}
