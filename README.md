# go-server

## 安装  
```text
go get github.com/zboyco/go-server
```

## 简单使用方法
默认使用换行符`\n`拆分数据包
```go
// main
func main() {
	// 新建服务
	mainServer := goserver.New("", 9043)
	// 注册OnMessage事件
	mainServer.SetOnMessage(onMessage)
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
```
## 自定义拆包协议
go-server 采用标准库`bufio.Scanner`实现数据拆包，默认使用`ScanLines`实现换行符拆包，支持自定义拆包规则，可以根据自己的需求制定，只需要自定义一个`bufio.SplitFunc`方法即可。  
假设我们采用 `head`+`body`的方式定义package，并指定第1个字节是`'$'`，第4个字节是`'#'`,第2、3位两个字节使用`int16`存储`body`长度，例子如下：
```go
func main() {
	// 新建服务
	mainServer := goserver.New("", 9043)
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
func onMessage(client *goserver.AppSession, token []byte) {
	// 将bytes转为字符串
	result := string(token)
	// 输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)
	// 发送给客户端
	client.Send([]byte("Got!"))
}
```
## 使用命令方式调用方法
上面的使用方法，我们都将接收到的消息放在一个`onMessage`中处理，而多数时候，我们希望将不同的请求使用不同的方法处理，go-server 提供了一种方式，配合`ReceiveFilter`过滤器 和`Action`处理模块，可以实现不同请求调用不同方法。  

`ReceiveFilter`过滤器有两个方法,`splitFunc`负责拆包,`resolveAction`负责将每一个`package`解析成`ActionName`和`Message`两个部分;  

`Action`处理模块负责注册方法到go-server中,供go-server调用;
> go-server 默认提供了两种常用的过滤器,分别为 `开始结束标记`和`固定头协议` 两种,也可以自定义过滤器,只需要实现`ReceiveFilter`接口即可  
> 自定义过滤器的方法可以参考[socket.go文件](https://github.com/zboyco/go-server/blob/master/socket.go)  
> `Action`模块可以注册多个,只要调用`模块根路径(ReturnRootPath)`+`方法名`没有重复即可，如有重复，在注册的时候会返回错误提示。  

下面用一个例子演示命令方式调用:  
server端:
```go
func main() {
	// 新建服务
	mainServer := goserver.New("", 9043)
	// 开始结束标记过滤器
	mainServer.SetReceiveFilter(&go_server.BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	})
	// 固定头部协议过滤器
	//mainServer.SetReceiveFilter(&goserver.FixedHeaderReceiveFilter{})
	// 注册OnError事件
	mainServer.SetOnError(onError)
	// 注册Action
	err := mainServer.RegisterAction(&module{})
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

// 实现ReturnRootPath方法,返回调用根路径
func (m *module) ReturnRootPath() string {
	return "v1"
}

// 定义命令
// 调用路径即 /v1/Say
func (m *module) Say(client *goserver.AppSession, token []byte) {
	//将bytes转为字符串
	result := string(token)
	//输出结果
	log.Println("接收到客户[", client.ID, "]数据:", result)
	// 发送给客户端
	client.Send([]byte("Got!"))
}
```
client端:
```go
func SendByBeginEndMark(conn net.Conn, msg string) error {
	begin := []byte{'!', '$'}
	end := []byte{'$', '!'}
	// 指定调用方法路径
	actionName := []byte("/v1/Say")

	var headBytes = make([]byte, 4)
	
	actionNameLength := len(actionName)
	content := []byte(msg)
	binary.BigEndian.PutUint32(headBytes, uint32(actionNameLength))
	_, err := conn.Write(begin)
	if err != nil {
		return err
	}
	_, err = conn.Write(headBytes)
	if err != nil {
		return err
	}
	_, err = conn.Write(actionName)
	if err != nil {
		return err
	}
	_, err = conn.Write(content)
	if err != nil {
		return err
	}
	_, err = conn.Write(end)
	if err != nil {
		return err
	}
	return nil
}
```

## 其他配置
go-server 另外提供两组方法和属性,如下所示。
### 三个设置通知的方法：
```go
// 用来输出错误信息
SetOnError(onErrorFunc func(error))
// 新会话连接通知
SetOnNewSessionRegister(onNewSessionRegisterFunc func(*AppSession))
// 会话关闭通知
SetOnSessionClosed(onSessionClosedFunc func(*AppSession, string))
```

### 三个获取在线会话的方法:
```go
// 通过ID获取会话
GetSessionByID(id string) (*AppSession, error)
// 通过属性获取会话
GetSessionByAttr(attr map[string]interface{}) <-chan *AppSession
// 获取所有会话
GetAllSessions() <-chan *AppSession
```
> 其中`GetSessionByAttr`返回所有属性值与传入参数有且想等的会话  
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

### 两个服务属性
```go
// 用于接收连接请求的协程数量，默认为2
mainServer.AcceptCount = 10

// 客户端空闲超时时间(秒)，默认300s,<=0则不设置超时
mainServer.IdleSessionTimeOut = 10
```


## 下面记录这个包实现的过程
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
    > 2. 通过`SetReceiveFilter`和`RegisterAction`实现类RPC协议  
    > - 默认实现了`标记数据包开始和结尾字节`和`固定头部协议`两种过滤器，亦可以通过实现`ReceiveFilter`接口来自定义过滤器  
    > - 使用方法参考`example`  
17. [扩展会话](https://github.com/zboyco/go-server/tree/step-17)  
    > 会话添加自定义属性，实现增加、设置、删除属性方法  
    会话增加`IsClosed`属性，方便判断当前会话是否已关闭  
    实现通过ID获取指定会话方法（返回会话）  
    实现获取所有会话方法（返回会话channel）  
    实现通过属性获取会话方法（返回会话channel）  
    - 包名修改为`goserver`,发布第一`tag`  