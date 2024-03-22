package goserver

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/zboyco/go-server/filter"
)

type Network string

const (
	TCP Network = "tcp"
	UDP Network = "udp"
)

// Server 服务结构
type Server struct {
	network                    Network       // 传输协议
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

	ioEOF               []byte                                                        // IO结束标记
	connectionFilterTCP []filter.ConnectionFilterTCP                                  // TCP连接过滤器
	connectionFilterUDP []filter.ConnectionFilterUDP                                  // UDP连接过滤器
	splitFunc           bufio.SplitFunc                                               // 拆包规则
	resolveAction       func(token []byte) (actionName string, msg []byte, err error) // 解析请求方法
	maxScanTokenSize    int                                                           // 最大拆包大小
	middlewaresBefore   Middlewares                                                   // action执行前中间件
	middlewaresAfter    Middlewares                                                   // action执行后中间件
	sendPacketFilter    Middlewares                                                   // 发送数据过滤
	actions             map[string][]ActionFunc                                       // 消息处理方法字典

	routers map[string][][]string // 用于启动时输出路由表
}

func newServer(network Network, ip string, port int, config *tls.Config) *Server {
	return &Server{
		network: network,
		ip:      ip,
		port:    port,
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

	switch server.network {
	case TCP:
		server.startTCP(addr)
	case UDP:
		server.startUDP(addr)
	default:
		log.Println("未知的传输协议：", server.network)
		return
	}
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

func (server *Server) handleOnError(err error) {
	log.Println(err)
	if server.onError != nil {
		server.onError(err)
	}
}

// closeSession 关闭session
func (server *Server) closeSession(session *AppSession, reason string) {
	// 如果设置了ioEOF，尝试发送
	if len(server.ioEOF) != 0 {
		_ = session.Send(server.ioEOF)
	}
	go session.Close(reason)
	// 从池中移除
	go server.sessionSource.deleteSession(session)
}

// SetEOF 设置IO结束标记
// 设置后，服务器关闭客户端时，会尝试发送此标记
func (server *Server) SetEOF(ioEOF []byte) {
	server.ioEOF = ioEOF
}

// SetSplitFunc 设置数据拆包方法
func (server *Server) SetSplitFunc(splitFunc bufio.SplitFunc) {
	server.splitFunc = splitFunc
}

// SetReceiveFilter 设置过滤器
func (server *Server) SetReceiveFilter(s filter.ReceiveFilter) {
	server.splitFunc = s.SplitFunc()
	server.resolveAction = s.ResolveAction()
}

// SetMaxScanTokenSize 设置数据最大长度
func (server *Server) SetMaxScanTokenSize(maxScanTokenSize int) {
	server.maxScanTokenSize = maxScanTokenSize
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

// RegisterConnectionFilterTCP 注册TCP连接过滤器
func (server *Server) RegisterConnectionFilterTCP(connectionFilter ...filter.ConnectionFilterTCP) {
	server.connectionFilterTCP = connectionFilter
}

// RegisterConnectionFilterUDP 注册UDP连接过滤器
func (server *Server) RegisterConnectionFilterUDP(connectionFilter ...filter.ConnectionFilterUDP) {
	server.connectionFilterUDP = connectionFilter
}

// RegisterSendPacketFilter 注册发送数据包过滤器
func (server *Server) RegisterSendPacketFilter(mids Middlewares) {
	server.sendPacketFilter = mids
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
