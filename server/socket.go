package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"time"
)

// Server 服务结构
type Server struct {
	ip                       string         // 服务器IP
	port                     int            // 服务器端口
	clientCounter            int64          // 计数器
	sessionSource            *sessionSource // Session池
	idleSessionTimeOut       int            // 客户端空闲超时时间
	clearIdleSessionInterval int            // 清空空闲会话的时间间隔,为0则不清理

	OnError              func(error)               // 错误方法
	OnMessage            func(*AppSession, []byte) // 接收到新消息
	OnNewSessionRegister func(*AppSession)         // 新客户端接入
	OnSessionClosed      func(*AppSession, string) // 客户端关闭通知
}

// New 新建一个服务
func New(ip string, port int, idleSessionTimeOut int, clearIdleSessionInterval int) *Server {
	return &Server{
		ip:            ip,
		port:          port,
		clientCounter: 0,
		sessionSource: &sessionSource{
			source: make(map[int64]*AppSession),
			list:   make(chan *AppSession, 1000),
		},
		idleSessionTimeOut:       idleSessionTimeOut,
		clearIdleSessionInterval: clearIdleSessionInterval,
	}
}

// Start 开始监听
func (server *Server) Start() {
	if server.OnMessage == nil {
		log.Println("错误,未找到数据处理方法!")
		return
	}

	// 定义一个本机端口
	localAddress, _ := net.ResolveTCPAddr("tcp4", fmt.Sprintf("%s:%d", server.ip, server.port))

	// 监听端口
	tcpListener, err := net.ListenTCP("tcp", localAddress)

	if err != nil {
		log.Println("监听出错, ", err)
		if server.OnError != nil {
			server.OnError(err)
		}
		return
	}

	// 程序返回后关闭socket
	defer tcpListener.Close()

	// 开启Session注册
	server.startRegisterSession()

	// 开启定时清理Session方法
	server.startClearTimeoutSession(server.idleSessionTimeOut, server.clearIdleSessionInterval)

	for {
		log.Println("等待客户端连接...")

		// 开始接收连接
		conn, err := tcpListener.Accept()

		if err != nil {
			log.Println("客户端连接失败, ", err)
			if server.OnError != nil {
				server.OnError(err)
			}
			continue
		}

		// 客户端ID数+1
		server.clientCounter++

		appSession := &AppSession{
			ID:             server.clientCounter,
			conn:           conn,
			activeDateTime: time.Now(),
		}

		// 启用goroutine处理
		go server.handleClient(appSession)
	}
}

// startRegisterSession 注册session
func (server *Server) startRegisterSession() {
	go server.sessionSource.registerSession()
}

// startClearTimeoutSession 周期性清理闲置Seesion
func (server *Server) startClearTimeoutSession(timeoutSecond int, interval int) {
	go server.sessionSource.clearTimeoutSession(timeoutSecond, interval)
}

// // 读取数据
// func handleClient(server *Server, session *AppSession) {
// 	//获取连接地址
// 	remoteAddr := session.conn.RemoteAddr()

// 	log.Println("客户端[", session.ID, "]地址:", remoteAddr)

// 	for {
// 		log.Println("等待接收客户端[", session.ID, "]的数据...", session.activeDateTime)

// 		bytes, err := session.Read()

// 		if err != nil {
// 			log.Println("客户端[", session.ID, "]数据接收错误, ", err)
// 			if server.OnError != nil {
// 				server.OnError(err)
// 			}
// 			session.conn.Close()
// 			log.Println("客户端[", session.ID, "]连接已关闭!")
// 			return
// 		}
// 		server.OnMessage(session, bytes)
// 	}
// }

// handleClient 读取数据
func (server *Server) handleClient(session *AppSession) {
	// 获取连接地址
	remoteAddr := session.conn.RemoteAddr()

	log.Println("客户端[", session.ID, "]地址:", remoteAddr)

	// 新客户端接入通知
	if server.OnNewSessionRegister != nil {
		server.OnNewSessionRegister(session)
	}

	// 注册Session
	server.sessionSource.addSession(session)

	// 创建scanner
	scanner := bufio.NewScanner(session.conn)

	//根据协议定义分离规则
	split := func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF {
			return 0, nil, errors.New("EOF")
		}
		if data[0] != '$' || data[3] != '#' {
			return 0, nil, errors.New("数据异常")
		}
		if len(data) > 4 {
			length := int16(0)
			binary.Read(bytes.NewReader(data[1:3]), binary.BigEndian, &length)
			if int(length)+4 <= len(data) {
				return int(length) + 4, data[4 : int(length)+4], nil
			}
		}
		return 0, nil, nil
	}

	// 设置分离函数
	scanner.Split(split)

	// 获取数据
	for scanner.Scan() {
		server.OnMessage(session, scanner.Bytes())
	}

	// 错误处理
	if err := scanner.Err(); err != nil {
		log.Println("客户端[", session.ID, "]数据接收错误, ", err)
		server.closeSession(session, err.Error())
	}
}

// closeSession 关闭session
func (server *Server) closeSession(session *AppSession, reason string) {
	session.Close(reason)

	// 从池中移除
	server.sessionSource.deleteSession(session.ID)
}
