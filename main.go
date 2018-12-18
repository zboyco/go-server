package main

import (
	"fmt"

	"go-server/server"
)

func main() {
	fmt.Println("hello golang!!!")

	mainServer := server.New("127.0.0.1", 9043)

	mainServer.OnMessage = onMessage

	mainServer.OnError = onError

	mainServer.Start()
}

func onMessage(bytes []byte) {
	//将bytes转为字符串
	result := string(bytes)

	//输出结果
	fmt.Println("接收到数据:", result)
}

func onError(err error) {
	//输出结果
	fmt.Println("错误: ", err)
}
