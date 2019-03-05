package main

import (
	"log"

	"github.com/zboyco/go-server/server"
)

func main() {

	mainServer := server.New("127.0.0.1", 9043, 10, 6)

	mainServer.OnMessage = onMessage

	mainServer.OnError = onError

	mainServer.Start()
}

// 接收数据方法
func onMessage(client *server.AppSession, bytes []byte) {
	//将bytes转为字符串
	result := string(bytes)

	//输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)

	// client.Send([]byte("Got!"))
}

// 接收错误方法
func onError(err error) {
	//输出结果
	log.Println("错误: ", err)
}
