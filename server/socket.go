package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// 服务结构
type Server struct {
	ip            string
	port          int
	clientCounter int64
	OnError       func(error)
	OnMessage     func(*AppSession, []byte)
}

// 新建一个服务
func New(ip string, port int) *Server {
	return &Server{
		ip:            ip,
		port:          port,
		clientCounter: 0,
	}
}

// 开始监听
func (server *Server) Start() {
	if server.OnMessage == nil {
		fmt.Println("错误,未找到数据处理方法!")
		return
	}

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
		fmt.Println("等待客户端连接...")

		//开始接收连接
		conn, err := tcpListener.Accept()

		if err != nil {
			fmt.Println("客户端连接失败, ", err)
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

// // 读取数据
// func handleClient(server *Server, session *AppSession) {
// 	//获取连接地址
// 	remoteAddr := session.conn.RemoteAddr()

// 	fmt.Println("客户端[", session.ID, "]地址:", remoteAddr)

// 	for {
// 		fmt.Println("等待接收客户端[", session.ID, "]的数据...", session.activeDateTime)

// 		bytes, err := session.Read()

// 		if err != nil {
// 			fmt.Println("客户端[", session.ID, "]数据接收错误, ", err)
// 			if server.OnError != nil {
// 				server.OnError(err)
// 			}
// 			session.conn.Close()
// 			fmt.Println("客户端[", session.ID, "]连接已关闭!")
// 			return
// 		}
// 		server.OnMessage(session, bytes)
// 	}
// }

// 读取数据
func handleClient(server *Server, session *AppSession) {
	// 获取连接地址
	remoteAddr := session.conn.RemoteAddr()

	fmt.Println("客户端[", session.ID, "]地址:", remoteAddr)

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
		fmt.Println("客户端[", session.ID, "]数据接收错误, ", err)
		session.conn.Close()
		fmt.Println("客户端[", session.ID, "]连接已关闭!")
	}
}
