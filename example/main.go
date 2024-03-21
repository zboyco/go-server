package main

import (
	"fmt"
	"log"
	"time"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/filter"
)

func main() {
	// 新建服务
	mainServer := goserver.New("", 9043)
	// 设置Socket接收协程数量
	// mainServer.AcceptCount = 10
	// 设置会话闲置超时时间，为0则不超时
	mainServer.IdleSessionTimeOut = 10

	// mainServer.RegisterConnectionFilter(func(conn net.Conn) error {
	// 	log.Println("连接", conn.RemoteAddr().String())
	// 	return errors.New("test filter")
	// })

	// 根据协议定义拆包规则
	// mainServer.SetSplitFunc(func(data []byte, atEOF bool) (int, []byte, error) {
	// 	if atEOF {
	// 		return 0, nil, nil
	// 	}
	// 	if data[0] != '$' || data[3] != '#' {
	// 		return 0, nil, errors.New("数据异常")
	// 	}
	// 	if len(data) > 4 {
	// 		length := int16(0)
	// 		binary.Read(bytes.NewReader(data[1:3]), binary.BigEndian, &length)
	// 		if int(length)+4 <= len(data) {
	// 			return int(length) + 4, data[4 : int(length)+4], nil
	// 		}
	// 	}
	// 	return 0, nil, nil
	// })

	mainServer.SetReceiveFilter(&filter.BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	})

	// mainServer.SetReceiveFilter(&goserver.FixedHeaderReceiveFilter{})

	err := mainServer.Action("/test", func(client *goserver.AppSession, msg []byte) ([]byte, error) {
		// 将bytes转为字符串
		result := string(msg)

		// 输出结果
		log.Println("test接收到客户[", client.ID, "]数据:", result)
		// 发送给客户端
		// client.Send([]byte("Got!"))
		return []byte("Got!"), nil
	})
	if err != nil {
		log.Panic(err)
	}

	mainServer.RegisterBeforeMiddlewares(goserver.Middlewares{
		func(client *goserver.AppSession, token []byte) ([]byte, error) {
			return []byte(string(token) + "-before1-"), nil
		},
		func(client *goserver.AppSession, token []byte) ([]byte, error) {
			return []byte(string(token) + "-before2-"), nil
		},
	})

	mainServer.RegisterAfterMiddlewares(goserver.Middlewares{
		func(client *goserver.AppSession, token []byte) ([]byte, error) {
			return []byte(string(token) + "-after3-"), nil
		},
		func(client *goserver.AppSession, token []byte) ([]byte, error) {
			return []byte(string(token) + "-after4-"), nil
		},
	})

	err = mainServer.RegisterModule(&module{})
	if err != nil {
		log.Panic(err)
	}

	err = mainServer.RegisterModule(&otherModule{name: "-name-"})
	if err != nil {
		log.Panic(err)
	}

	// 注册OnMessage事件
	mainServer.SetOnMessage(onMessage)
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
func onMessage(client *goserver.AppSession, token []byte) ([]byte, error) {
	// 将bytes转为字符串
	result := string(token)

	// 输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)
	// 发送给客户端
	return []byte("Got!"), nil
}

// 接收错误方法
func onError(err error) {
	// 输出结果
	log.Println("错误: ", err)
}

type module struct{}

func (m *module) Root() string {
	return "/"
}

func (m *module) Summary() string {
	return "这个是模块的摘要信息"
}

func (m *module) Say(client *goserver.AppSession, token []byte) ([]byte, error) {
	// 将bytes转为字符串
	result := string(token)

	// 输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)

	return token, nil
}

type otherModule struct {
	name string
}

func (m *otherModule) Root() string {
	return "/v2"
}

func (m *otherModule) Print(client *goserver.AppSession, token []byte) ([]byte, error) {
	// 将bytes转为字符串
	result := string(token)

	// 输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)

	return []byte(result + m.name), nil
}

func (m *otherModule) MiddlewaresBeforeAction() goserver.Middlewares {
	return goserver.Middlewares{
		func(client *goserver.AppSession, token []byte) ([]byte, error) {
			return []byte(string(token) + "-before3-"), nil
		},
		func(client *goserver.AppSession, token []byte) ([]byte, error) {
			return []byte(string(token) + "-before4-"), nil
		},
	}
}

func (m *otherModule) MiddlewaresAfterAction() goserver.Middlewares {
	return goserver.Middlewares{
		func(client *goserver.AppSession, token []byte) ([]byte, error) {
			return []byte(string(token) + "-after1-"), nil
		},
		func(client *goserver.AppSession, token []byte) ([]byte, error) {
			return []byte(string(token) + "-after2-"), nil
		},
	}
}
