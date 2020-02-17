package main

import (
	"fmt"
	"github.com/zboyco/go-server"
	"log"
	"time"
)

func main() {
	// 新建服务
	mainServer := goserver.New("", 9043)
	// 设置Socket接收协程数量
	mainServer.AcceptCount = 10
	// 设置会话闲置超时时间，为0则不超时
	mainServer.IdleSessionTimeOut = 10

	// 根据协议定义拆包规则
	//mainServer.SetSplitFunc(func(data []byte, atEOF bool) (int, []byte, error) {
	//	if atEOF {
	//		return 0, nil, nil
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

	//mainServer.SetReceiveFilter(&go_server.BeginEndMarkReceiveFilter{
	//	Begin: []byte{'!', '$'},
	//	End:   []byte{'$', '!'},
	//})

	mainServer.SetReceiveFilter(&goserver.FixedHeaderReceiveFilter{})

	err := mainServer.Action("/test", func(client *goserver.AppSession, msg []byte) {
		// 将bytes转为字符串
		result := string(msg)

		// 输出结果
		log.Println("test接收到客户[", client.ID, "]数据:", result)
		// 发送给客户端
		client.Send([]byte("Got!"))
	})
	if err != nil {
		log.Panic(err)
	}

	err = mainServer.RegisterAction(&module{})
	if err != nil {
		log.Panic(err)
	}

	err = mainServer.RegisterAction(&otherModule{})
	if err != nil {
		log.Panic(err)
	}

	// 注册OnMessage事件
	//mainServer.SetOnMessage(onMessage)
	// 注册OnError事件
	mainServer.SetOnError(onError)

	go func() {
		counter := 0
		for {
			time.Sleep(10 * time.Second)
			counter++
			sessions := mainServer.GetAllSessions()
			for {
				session, ok := <-sessions
				if !ok {
					break
				}
				session.Send([]byte(fmt.Sprintf("server to client [%v]: %v", session.ID, counter)))
			}
		}
	}()
	// 开启服务
	mainServer.Start()
}

// 接收数据方法
func onMessage(client *goserver.AppSession, token []byte) {
	// 将bytes转为字符串
	result := string(token)

	// 输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)
	// 发送给客户端
	client.Send([]byte("Got!"))
}

// 接收错误方法
func onError(err error) {
	//输出结果
	log.Println("错误: ", err)
}

type module struct {
}

func (m *module) ReturnRootPath() string {
	return ""
}

func (m *module) Say(client *goserver.AppSession, token []byte) {
	//将bytes转为字符串
	result := string(token)

	//输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)

	client.Send([]byte("Got!"))
}

type otherModule struct {
}

func (m *otherModule) ReturnRootPath() string {
	return "v2"
}

func (m *otherModule) Print(client *goserver.AppSession, token []byte) {
	//将bytes转为字符串
	result := string(token)

	//输出结果
	log.Println("Print接收到客户[", client.ID, "]数据:", result)

	client.Send([]byte("Got!"))
}
