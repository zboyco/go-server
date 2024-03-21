package goserver

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Server 服务结构
type Server struct {
	ip                         string        // 服务器IP
	port                       int           // 服务器端口
	sessionSource              *sessionPool  // Session池
	idleSessionTimeOutDuration time.Duration // 超时持续时间，用于设置deadline
	tlsConfig                  *tls.Config   // tls配置

	AcceptCount        int // 用于接收连接请求的协程数量
	IdleSessionTimeOut int // 客户端空闲超时时间(秒)，默认300s,<=0则不设置超时

	onError              func(error)               // 错误方法
	onNewSessionRegister func(*AppSession)         // 新客户端接入
	onSessionClosed      func(*AppSession, string) // 客户端关闭通知

	connectionFilter  []ConnectionFilter                                            // 连接过滤器
	splitFunc         bufio.SplitFunc                                               // 拆包规则
	resolveAction     func(token []byte) (actionName string, msg []byte, err error) // 解析请求方法
	middlewaresBefore Middlewares                                                   // action执行前中间件
	middlewaresAfter  Middlewares                                                   // action执行后中间件
	actions           map[string][]ActionFunc                                       // 消息处理方法字典

	routers map[string][][]string // 用于启动时输出路由表
}

// New 新建一个tcp4服务
func New(ip string, port int) *Server {
	return NewWithTLS(ip, port, nil)
}

// NewWithTLS 新建一个tls加密服务
func NewWithTLS(ip string, port int, config *tls.Config) *Server {
	return &Server{
		ip:   ip,
		port: port,
		sessionSource: &sessionPool{
			list: make(chan *sessionHandle, 100),
		},
		IdleSessionTimeOut: 300,
		AcceptCount:        1,
		actions:            make(map[string][]ActionFunc),
		splitFunc:          bufio.ScanLines,
		tlsConfig:          config,

		routers: make(map[string][][]string),
	}
}

// Start 开始监听
func (server *Server) Start() {
	if server.splitFunc == nil {
		log.Println("use default split function")
		server.splitFunc = bufio.ScanLines
	}

	if len(server.actions) == 0 {
		log.Println("error: no message action")
		return
	}

	server.idleSessionTimeOutDuration = time.Duration(server.IdleSessionTimeOut) * time.Second

	var tcpListener net.Listener
	var err error

	addr := fmt.Sprintf("%s:%d", server.ip, server.port)
	if server.ip != "" && server.ip != "localhost" {
		ipAddr := net.ParseIP(server.ip)
		if ipAddr == nil {
			log.Println("ip地址不正确!", server.ip)
			return
		}
		if ipAddr.To4() == nil {
			addr = fmt.Sprintf("[%s]:%d", server.ip, server.port)
		}
	}
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
				go server.handleClient(conn)
			}
		}(i)
	}

	server.printServerInfo()

	wg.Wait()
}

func (server *Server) printServerInfo() {
	for k, v := range server.routers {
		fmt.Printf("[GO-SERVER] Source %s\n", k)
		for _, action := range v {
			fmt.Printf("[GO-SERVER]        %s", action[0])
			if action[1] != "" {
				fmt.Printf("   ==>   %s", action[1])
			}
			fmt.Print("\n")
		}
	}
	fmt.Printf("[GO-SERVER] Listen on %s:%d\n\n", server.ip, server.port)
}

// handleClient 读取数据
func (server *Server) handleClient(conn net.Conn) {
	// 连接过滤器
	if server.connectionFilter != nil {
		for i := range server.connectionFilter {
			if err := server.connectionFilter[i](conn); err != nil {
				log.Printf("connect[%s] filter because %s", conn.RemoteAddr(), err.Error())
				_ = conn.Close()
				return
			}
		}
	}

	// 创建会话对象
	session := &AppSession{
		ID:   uuid.NewString(),
		conn: conn,
		attr: make(map[string]interface{}),
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

func (server *Server) handleOnError(err error) {
	log.Println(err)
	if server.onError != nil {
		server.onError(err)
	}
}

// closeSession 关闭session
func (server *Server) closeSession(session *AppSession, reason string) {
	go session.Close(reason)
	// 从池中移除
	go server.sessionSource.deleteSession(session)
}

// SetSplitFunc 设置数据拆包方法
func (server *Server) SetSplitFunc(splitFunc bufio.SplitFunc) {
	server.splitFunc = splitFunc
}

// SetOnMessage 设置接收到新消息处理方法
func (server *Server) SetOnMessage(onMessageFunc ActionFunc) {
	server.actions[""] = []ActionFunc{onMessageFunc}
}

// SetOnError 设置接收到错误处理方法
func (server *Server) SetOnError(onErrorFunc func(error)) {
	server.onError = onErrorFunc
}

// SetOnNewSessionRegister 设置新会话加入时处理方法
func (server *Server) SetOnNewSessionRegister(onNewSessionRegisterFunc func(*AppSession)) {
	server.onNewSessionRegister = onNewSessionRegisterFunc
}

// SetOnSessionClosed 设置会话关闭时处理方法
func (server *Server) SetOnSessionClosed(onSessionClosedFunc func(*AppSession, string)) {
	server.onSessionClosed = onSessionClosedFunc
}

// RegisterConnectionFilter 注册连接过滤器
func (server *Server) RegisterConnectionFilter(connectionFilter ...ConnectionFilter) {
	server.connectionFilter = connectionFilter
}

// RegisterBeforeMiddlewares 注册aciton前中间件
func (server *Server) RegisterBeforeMiddlewares(mids Middlewares) {
	server.middlewaresBefore = mids
}

// RegisterAfterMiddlewares 注册action后中间件
func (server *Server) RegisterAfterMiddlewares(mids Middlewares) {
	server.middlewaresAfter = mids
}

// GetSessionByID 通过ID获取会话
func (server *Server) GetSessionByID(id string) (*AppSession, error) {
	return server.sessionSource.getSessionByID(id)
}

// GetSessionByAttr 通过属性条件获取会话
func (server *Server) GetSessionByAttr(cond ConditionFunc) <-chan *AppSession {
	return server.sessionSource.getSessionByAttr(cond)
}

// GetAllSessions 获取所有会话
func (server *Server) GetAllSessions() <-chan *AppSession {
	return server.sessionSource.getAllSessions()
}

// CountSessions 计算现有会话数量
func (server *Server) CountSessions() int {
	return server.sessionSource.count()
}
