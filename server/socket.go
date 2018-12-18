package server

import (
	"fmt"
	"net"
)

//服务结构
type Server struct {
	ip   string
	port int
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
			continue
		}

		//启用goroutine处理
		go handleClient(conn)
	}
}

func handleClient(conn net.Conn) {
	//获取连接地址
	remoteAddr := conn.RemoteAddr()

	fmt.Println("客户地址:", remoteAddr)

	//定义一个数据接收Buffer
	var buf [1024]byte

	for {
		fmt.Println("等待接收数据...")
		//读取数据
		n, err := conn.Read(buf[0:])

		if err != nil {
			fmt.Println("数据接收错误, ", err)
			return
		}

		//将bytes转为字符串
		result := string(buf[0:n])

		//输出结果
		fmt.Println("接收到数据:", result)
	}
}
