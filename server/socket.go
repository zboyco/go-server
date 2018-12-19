package server

import (
	"fmt"
	"net"
	"time"
)

//Server 服务结构
type Server struct {
	ip            string
	port          int
	clientCounter int64
	OnError       func(error)
	OnMessage     func(*AppSession, []byte)
}

//New 新建一个服务
func New(ip string, port int) *Server {
	return &Server{
		ip:            ip,
		port:          port,
		clientCounter: 0,
	}
}

//Start 开始监听
func (server *Server) Start() {
	//定义一个本机端口
	localAddress, _ := net.ResolveTCPAddr("tcp4", fmt.Sprintf("%s:%d", server.ip, server.port))

	//监听端口
	tcpListener, err := net.ListenTCP("tcp", localAddress)

	if err != nil {
		fmt.Println("监听出错, ", err)
		if server.OnError != nil {
			server.OnError(err)
		}
		return
	}

	//程序返回后关闭socket
	defer tcpListener.Close()

	for {
		fmt.Println("等待客户连接...")

		//开始接收连接
		conn, err := tcpListener.Accept()

		if err != nil {
			fmt.Println("客户连接失败, ", err)
			if server.OnError != nil {
				server.OnError(err)
			}
			continue
		}

		//客户端ID数+1
		server.clientCounter++

		appSession := &AppSession{
			ID:             server.clientCounter,
			conn:           conn,
			activeDateTime: time.Now(),
			buffer:         newBuffer(conn, 1024*512),
		}

		//启用goroutine处理
		go handleClient(server, appSession)
	}
}

//handleClient 读取数据
func handleClient(server *Server, session *AppSession) {
	//获取连接地址
	remoteAddr := session.conn.RemoteAddr()

	fmt.Println("客户[", session.ID, "]地址:", remoteAddr)

	for {
		fmt.Println("等待接收客户[", session.ID, "]的数据...", session.activeDateTime)

		bytes, err := session.Read()

		if err != nil {
			fmt.Println("客户[", session.ID, "]数据接收错误, ", err)
			if server.OnError != nil {
				server.OnError(err)
			}
			return
		}

		if server.OnMessage == nil {
			fmt.Println("错误,未找到数据处理方法!")
			continue
		}
		server.OnMessage(session, bytes)
	}
}
