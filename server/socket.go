package server

import (
	"fmt"
	"net"
)

//服务结构
type Server struct {
	ip        string
	port      int
	OnError   func(error)
	OnMessage func([]byte)
}

//新建一个服务
func New(ip string, port int) *Server {
	return &Server{
		ip:   ip,
		port: port,
	}
}

//开始监听
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

		//启用goroutine处理
		go handleClient(server, conn)
	}
}

//读取数据
func handleClient(server *Server, conn net.Conn) {
	//获取连接地址
	remoteAddr := conn.RemoteAddr()

	fmt.Println("客户地址:", remoteAddr)

	//定义一个数据接收Buffer
	var buf [10240]byte

	for {
		fmt.Println("等待接收数据...")
		//读取数据,io.Reader 需要传入一个byte切片
		n, err := conn.Read(buf[0:])

		if err != nil {
			fmt.Println("数据接收错误, ", err)
			if server.OnError != nil {
				server.OnError(err)
			}
			return
		}

		if server.OnMessage == nil {
			fmt.Println("错误,未找到数据处理方法!")
			continue
		}
		server.OnMessage(buf[0:n])
	}
}
