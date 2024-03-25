# go-server
项目地址：[https://github.com/zboyco/go-server](https://github.com/zboyco/go-server)  

go-server 是我在学习golang的过程中，从最简单的socket一步一步改造形成的。  

目前功能如下：  
1. 普通的socket功能，支持 tcp 和 udp，支持ip4和ip6  
2. 使用标准库`bufio.Scanner`实现拆包，可以直接使用`bufio.Scanner`内置的拆包协议，当然也可以自定义拆包协议  
3. 提供普通`OnMessage`和命令路由两种使用模式  
4. 提供单个`Action`添加路由方法,同时也采用实现`ActionModule`接口的方式批量添加路由  
5. 过滤器支持自定义，只需实现`ReceiveFilter`接口  
6. 支持设置会话超时时间，超时的会话会自动关闭  
7. 提供会话查找方法，可以根据ID或自定义属性查找会话  
8. 支持tls  
9. 提供简单的客户端实现[github.com/zboyco/go-server/client]
10. ...  

问题如下：  
1. ...原谅我不会写文档 (╥╯^╰╥)  
2. 有什么问题大家随便留言  
3. ...

# 使用方法
## 安装  
```text
go get github.com/zboyco/go-server
```

## 简单使用
默认使用换行符`\n`拆分数据包  
```go
// main
func main() {
	// 新建服务
	mainServer := goserver.New(goserver.TCP, "", 8080)
	// 注册OnMessage事件
	mainServer.SetOnMessage(onMessage)
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
	// client.Send([]byte("Got!"))
	return []byte("Got!"), nil
}
```
## 使用tls
使用`NewWithTLS`方法新建一个tls服务  
```go
	crt, err := tls.LoadX509KeyPair("server.crt", "server.key")
	if err != nil {
		log.Fatalln(err.Error())
	}
	// 新建服务
	mainServer := goserver.NewWithTLS("", 8080, &tls.Config{
		Certificates: []tls.Certificate{crt},
	})
```
## 自定义拆包协议
go-server 采用标准库`bufio.Scanner`实现数据拆包，默认使用`ScanLines`实现换行符拆包，支持自定义拆包规则，可以根据自己的需求制定，只需要自定义一个`bufio.SplitFunc`方法即可。  
假设我们采用 `head`+`body`的方式定义package，并指定第1个字节是`'$'`，第4个字节是`'#'`,第2、3位两个字节使用`int16`存储`body`长度，例子如下：
```go
func main() {
	// 新建服务
	mainServer := goserver.New(goserver.TCP, "", 8080)
	// 根据协议定义拆包规则
	mainServer.SetSplitFunc(func(data []byte, atEOF bool) (int, []byte, error) {
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
	})
	// 注册OnMessage事件
	mainServer.SetOnMessage(onMessage)
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
	// client.Send([]byte("Got!"))
	return []byte("Got!"), nil
}
```
## 使用命令路由方式调用方法
上面的使用方法，我们都将接收到的消息放在一个`onMessage`中处理，而多数时候，我们希望将不同的请求使用不同的方法处理，go-server 提供了一种方式，配合`ReceiveFilter`过滤器 和`ActionModule`处理模块，可以实现不同请求调用不同方法。  

`ReceiveFilter`过滤器有两个方法,`splitFunc`负责拆包,`resolveAction`负责将每一个`package`解析成`ActionName`和`Message`两个部分;  

`ActionModule`处理模块负责注册方法到go-server中,供go-server调用;
> go-server 默认提供了两种常用的过滤器,分别为 `开始结束标记`和`固定头协议` 两种,也可以自定义过滤器,只需要实现`filter.ReceiveFilter`接口即可，自定义过滤器的方法参考[begin_end.go文件](https://github.com/zboyco/go-server/blob/master/filter/begin_end.go)    
> `ActionModule`模块可以注册多个,只要调用`模块根路径(Root)`+`方法名`没有重复即可，如有重复，在注册的时候会返回错误提示。  
> 注意实现`ActionModule`模块的方法名要以大写字母开头  

下面用一个例子演示命令方式调用:  
server端:
```go
func main() {
	// 新建服务
	mainServer := goserver.New(goserver.TCP, "", 8080)
	// 开始结束标记过滤器
	mainServer.SetReceiveFilter(&filter.BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	})
	// 固定头部协议过滤器
	//mainServer.SetReceiveFilter(&filter.FixedHeaderReceiveFilter{})
	// 注册OnError事件
	mainServer.SetOnError(onError)

	// 添加单个Action
	err := mainServer.Action("/test", func(client *goserver.AppSession, msg []byte) ([]byte, error) {
		// 将bytes转为字符串
		result := string(msg)
		// 输出结果
		log.Println("单独添加Action 接收到客户[", client.ID, "]数据:", result)
		// 发送给客户端
		// client.Send([]byte("Got!"))
		return []byte("Got!"), nil
	})

	// 使用模块注册Action
	err = mainServer.RegisterModule(&module{})
	if err != nil {
		log.Panic(err)
	}
	// 开启服务
	mainServer.Start()
}

// 接收错误方法
func onError(err error) {
	//输出结果
	log.Println("错误: ", err)
}

// 定义Action模块
type module struct {
}

// 实现Root方法,返回调用根路径
func (m *module) Root() string {
	return "/v1"
}

// 定义命令
// 注意方法名要以大写字母开头
// 调用路径即 /v1/say
func (m *module) Say(client *goserver.AppSession, token []byte) ([]byte, error) {
	//将bytes转为字符串
	result := string(token)
	//输出结果
	log.Println("Say方法 接收到客户[", client.ID, "]数据:", result)
	// 发送给客户端
	// client.Send([]byte("Got!"))
	return []byte("Got!"), nil
}
```
client端:
```go
func SendByBeginEndMark(msg []byte) error {
	filter := &filter.BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	}
	c := client.NewBeginEndMarkClient(goserver.TCP, "", 8080, filter)

	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}

	// 指定调用方法路径
	return c.SendAction("/v1/say", msg)
}
```

## 自定义发送数据包过滤器
因为某些情况下，服务器收包和发包对协议的定义不一定一致，可以通过设置goserver主体的SendPacketFilter来实现服务器向客户端发送数据包时的封包协议，也可以通过方法过滤发送的数据包内容。
```go
func main() {
	// 新建服务
	mainServer := goserver.New(goserver.TCP, "", 8080)
	// 开始结束标记过滤器
	mainServer.SetReceiveFilter(&filter.BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	})
	// 注册发送数据包过滤器
	// 该示例设置为发送包封包与服务器拆包协议不同
	mainServer.RegisterSendPacketFilter(goserver.Middlewares{
		func(as *goserver.AppSession, b []byte) ([]byte, error) {
			return bytes.Join([][]byte{{'#', '$'}, b, {'$', '#'}}, nil), nil
		},
	})
	// 注册OnError事件
	mainServer.SetOnError(onError)

	// 使用模块注册Action
	err = mainServer.RegisterModule(&module{})
	if err != nil {
		log.Panic(err)
	}
	// 开启服务
	mainServer.Start()
}
```


## 中间件  
goserver主体和ActionModule可以注册使用中间件，各自有before和after两个事件，都是相对于实际的action。如下：
goserver主体，直接使用方法注册
```go
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
```
ActionModule，通过实现`MiddlewaresBeforeAction`或`MiddlewaresAfterAction`接口注册
```go
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
```

总执行顺序是 `server.before` -> `module.before` -> action -> `module.after` -> `server.after`


# 包结构介绍
## Server 服务
`Server`是一个go-server的基本结构，可以理解为一个`Server`就是一个socket服务，提供如下方法： 

 ### 1. 两个属性
```go
// 用于接收连接请求的协程数量，默认为2
mainServer.AcceptCount = 10

// 客户端空闲超时时间(秒)，默认300s,<=0则不设置超时
mainServer.IdleSessionTimeOut = 10
```
### 2. 数据处理
```go
// 设置数据拆包方法
SetSplitFunc(splitFunc bufio.SplitFunc)
// 设置数据包最大长度
SetMaxScanTokenSize(maxScanTokenSize int)
// 注册TCP连接过滤器
RegisterConnectionFilterTCP(connectionFilter ...filter.ConnectionFilterTCP)
// 注册UDP连接过滤器
RegisterConnectionFilterUDP(connectionFilter ...filter.ConnectionFilterUDP)
// 设置接收到新消息处理方法
SetOnMessage(onMessageFunc ActionFunc)
// 注册发送数据包过滤器
RegisterSendPacketFilter(mids Middlewares)
// 注册Action前置中间件
RegisterBeforeMiddlewares(mids Middlewares)
// 注册Action后置中间件
RegisterAfterMiddlewares(mids Middlewares)
// 设置IO结束标记，设置后，服务器关闭客户端时，会尝试发送此标记
SetEOF(ioEOF []byte)
```
### 3. 命令路由
```go
// 设置过滤器
SetReceiveFilter(s ReceiveFilter)
// 添加单个命令路由方法
Action(path string, actionFunc ...ActionFunc) error
// 注册方法处理模块（命令路由）
RegisterModule(m ActionModule) error
```
### 4. 三个设置通知的方法：
```go
// 设置输出错误信息方法
SetOnError(onErrorFunc func(error))
// 设置新会话连接通知
SetOnNewSessionRegister(onNewSessionRegisterFunc func(*AppSession))
// 设置会话关闭通知
SetOnSessionClosed(onSessionClosedFunc func(*AppSession, string))
```
### 5. 三个获取在线会话的方法:
```go
// 通过ID获取会话
GetSessionByID(id string) (*AppSession, error)
// 获取所有会话
GetAllSessions() <-chan *AppSession
// 通过属性条件获取会话
// type ConditionFunc func(map[string]interface{}) bool
GetSessionByAttr(cond ConditionFunc) <-chan *AppSession
```
> `GetSessionByAttr`返回所有ConditionFunc为true的会话  
> `GetSessionByAttr`和`GetAllSessions`都返回一个无缓冲的`channel`  

example:
```go
sessions := mainServer.GetAllSessions()
for {
	session, ok := <-sessions
	if !ok {
		break
	}
	session.Send([]byte(fmt.Sprintf("server to client [%v]: hi~", session.ID)))
}
```
## AppSession 会话
`AppSession`是go-server中封装的会话结构，暴露以下两个属性：
```go
ID       string   // 连接唯一标识
IsClosed bool     // 标记会话是否关闭
```
`ID` 是会话的唯一标识，可以用来查找指定的会话；  
`IsClosed` 标记会话是否已经关闭，有需要时可以用来进行判断。  
另外`AppSession`还提供了一个可以设置自定义属性的`map[string]interface{}`，go-server可以通过自定义属性作为条件查询会话（上面已介绍`GetSessionByAttr`），通过以下4个方法可以直接操作会话的自定义属性：  
```go
// AddAttr 添加会话属性
AddAttr(key string, value interface{}) error

// SetAttr 设置会话属性
SetAttr(key string, value interface{})

// GetAttr 获取会话属性
GetAttr(key string) (interface{}, error)

// DelAttr 删除会话属性
DelAttr(key string) error
```

## 最后记录下这个包一步一步折腾的过程
1. [实现socket服务](https://github.com/zboyco/go-server/tree/step-1)  
    > 简单实现一个socket服务,能接收客户端连接并接收数据  
2. [循环读取数据](https://github.com/zboyco/go-server/tree/step-2)  
    > 利用for循环,等待客户端发送数据  
3. [利用goroutine实现同时多个客户端连接](https://github.com/zboyco/go-server/tree/step-3)  
    > 将数据读取放入单独的方法中,利用goroutine运行  
4. [将创建socket的方法放入单独的包中](https://github.com/zboyco/go-server/tree/step-4)  
5. [将读取的数据处理方法作为参数传入server中](https://github.com/zboyco/go-server/tree/step-5)  
    > OnMessage 和 OnError 通过Server结构公开变量传入  
6. [增加AppSession结构体](https://github.com/zboyco/go-server/tree/step-6)  
    > OnMessage返回AppSession结构体,提供Send方法,服务器可以主动向客户端发送数据  
7. [Session增加唯一ID,拆分socket中的Read方法](https://github.com/zboyco/go-server/tree/step-7)  
    > 增加ID为了以后判断闲置超时;  
    拆分Read方法方便扩展协议  
8. [session中Read方法实现粘包拆包处理](https://github.com/zboyco/go-server/tree/step-8)  
    > 定义简单协议,数据包头由4字节构成:  
    > - 第1位固定为'$'  
    > - 第2-3位为Body长度(uint16)  
    > - 第4位固定为'#'  
    > - 接收数据时若第1位和第4位不正确则认为接收到异常数据,同时关闭socket连接  
9. [采用标准库scanner实现数据分离处理粘包](https://github.com/zboyco/go-server/tree/step-9)  
    > 参考http://feixiao.github.io/2016/05/08/bufio/  
10. [添加Session容器,增加超时自动关闭Session功能](https://github.com/zboyco/go-server/tree/step-10)  
11. [修改Session注册采用通道,避免新会话接入阻塞](https://github.com/zboyco/go-server/tree/step-11)  
12. [优化部分代码，修改BUG](https://github.com/zboyco/go-server/tree/step-12)  
    > 修改`sessionID`为string类型，采用UUID  
    超时会话关闭后从会话池中移除  
    暴露数据流财拆包规则，用户可以自定义拆包规则
13. [修改BUG](https://github.com/zboyco/go-server/tree/step-13)  
    > 会话池从map修改为sync.Map类型  
    合并会话池的增加和移除操作，共用一个channel处理  
14. [修改参数](https://github.com/zboyco/go-server/tree/step-14)  
    > 修改监听协程数量可设置，默认为2  
    `appSession`增加关闭标识，防止`socket`重复`close`  
    修改`New`方法，将超时相关设置修改为参数设置  
15. [修改闲置超时处理方式](https://github.com/zboyco/go-server/tree/step-15)  
    > 采用`net.Conn`自带的`deadline`方式设置超时(主要是小白，以前不知道有这个)  
16. [实现普通拆包和路由两种方式](https://github.com/zboyco/go-server/tree/step-16)  
    > 1. 通过`SetSplitFunc`和`SetOnMessage`两个方法实现普通socket协议  
    > 2. 通过`SetReceiveFilter`和`RegisterModule`实现类RPC协议  
    > - 默认实现了`标记数据包开始和结尾字节`和`固定头部协议`两种过滤器，亦可以通过实现`ReceiveFilter`接口来自定义过滤器  
    > - 使用方法参考`example`  
17. [扩展会话](https://github.com/zboyco/go-server/tree/step-17)  
    > 会话添加自定义属性，实现增加、设置、删除属性方法  
    会话增加`IsClosed`属性，方便判断当前会话是否已关闭  
    实现通过ID获取指定会话方法（返回会话）  
    实现获取所有会话方法（返回会话channel）  
    实现通过属性获取会话方法（返回会话channel）  
    包名修改为`goserver`,发布第一个`tag`  
    增加命令路由单个注册方法`Action`  
18. [实现连接过滤器和数据中间件](https://github.com/zboyco/go-server)  
	> 实现连接过滤器，过滤器返回错误则会立即断开连接  
	增加数据中间件，分为服务中间件和模块中间件，各有before和after两个时间点，执行顺序为 `server.before`->`module.before`->`action`->`module.after`->`server.after`