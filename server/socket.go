package server

import (
	"bufio"
	"fmt"
	uuid "github.com/satori/go.uuid"
	"log"
	"net"
	"sync"
	"time"
)

// Server 服务结构
type Server struct {
	ip            string       // 服务器IP
	port          int          // 服务器端口
	sessionSource *sessionPool // Session池

	AcceptCount              int // 用于接收连接请求的协程数量
	IdleSessionTimeOut       int // 客户端空闲超时时间
	ClearIdleSessionInterval int // 清空空闲会话的时间间隔,为0则不清理

	SplitFunc            bufio.SplitFunc           // 拆包规则
	OnError              func(error)               // 错误方法
	OnMessage            func(*AppSession, []byte) // 接收到新消息
	OnNewSessionRegister func(*AppSession)         // 新客户端接入
	OnSessionClosed      func(*AppSession, string) // 客户端关闭通知
}

// New 新建一个服务
func New(ip string, port int) *Server {
	return &Server{
		ip:   ip,
		port: port,
		sessionSource: &sessionPool{
			list: make(chan *sessionHandle, 100),
		},
		IdleSessionTimeOut:       300,
		ClearIdleSessionInterval: 120,
		AcceptCount:              2,
	}
}

// Start 开始监听
func (server *Server) Start() {
	if server.SplitFunc == nil {
		log.Println("错误,未找到数据拆包方法!")
		return
	}

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

	// 开启会话池管理
	go server.sessionSource.sessionPoolManager()

	// 开启定时清理Session方法
	go server.sessionSource.clearTimeoutSession(server.IdleSessionTimeOut, server.ClearIdleSessionInterval)

	var wg sync.WaitGroup
	for i := 0; i < server.AcceptCount; i++ {
		wg.Add(1)
		go func(acceptIndex int) {
			defer wg.Done()
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
				// 启用goroutine处理
				go server.handleClient(conn)
			}
		}(i)
	}
	wg.Wait()
}

// handleClient 读取数据
func (server *Server) handleClient(conn net.Conn) {
	// 创建会话对象
	session := &AppSession{
		ID:             uuid.Must(uuid.NewV4()).String(),
		conn:           conn,
		activeDateTime: time.Now(),
	}
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

	// 设置分离函数
	scanner.Split(server.SplitFunc)

	// 获取数据
	for scanner.Scan() {
		//更新最后活跃时间
		session.activeDateTime = time.Now()
		server.OnMessage(session, scanner.Bytes())
	}

	// 错误处理
	if err := scanner.Err(); err != nil {
		server.closeSession(session, err.Error())
	}
}

// closeSession 关闭session
func (server *Server) closeSession(session *AppSession, reason string) {
	go session.Close(reason)
	// 从池中移除
	go server.sessionSource.deleteSession(session)
}
