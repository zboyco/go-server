package main

import (
	"github.com/zboyco/go-server"
	"log"
)

func main() {

	mainServer := go_server.New("", 9043)
	mainServer.AcceptCount = 10
	mainServer.IdleSessionTimeOut = 10

	//根据协议定义分离规则
	//mainServer.SetSplitFunc(func(data []byte, atEOF bool) (int, []byte, error) {
	//	if atEOF {
	//		return 0, nil, errors.New("EOF")
	//	}
	//	if data[0] != '$' || data[3] != '#' {
	//		return 0, nil, errors.New("数据异常")
	//	}
	//	if len(data) > 4 {
	//		length := int16(0)
	//		binary.Read(bytes.NewReader(data[1:3]), binary.BigEndian, &length)
	//		if int(length)+4 <= len(data) {
	//			return int(length) + 4, data[4 : int(length)+4], nil
	//		}
	//	}
	//	return 0, nil, nil
	//})

	mainServer.SetReceiveFilter(&go_server.BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	})

	//err := mainServer.RegisterAction(&module{})
	//if err != nil {
	//	log.Panic(err)
	//}

	//mainServer.SetOnMessage(onMessage)

	mainServer.SetOnError(onError)

	mainServer.Start()
}

// 接收数据方法
func onMessage(client *go_server.AppSession, token []byte) {
	//将bytes转为字符串
	result := string(token)

	//输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)

	client.Send([]byte("Got!"))
}

// 接收错误方法
func onError(err error) {
	//输出结果
	log.Println("错误: ", err)
}

type module struct {
}

func (m *module) ReturnPath() string {
	return ""
}

func (m *module) Say(client *go_server.AppSession, token []byte) {
	//将bytes转为字符串
	result := string(token)

	//输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)

	client.Send([]byte("Got!"))
}
