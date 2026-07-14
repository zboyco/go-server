# go-server

`go-server` 是一个基于 Go 标准库构建的 TCP/UDP 应用层网络库，面向需要自定义拆包、命令路由、会话管理和可控停机的服务端场景。

当前主线是破坏性升级后的 v1 API，要求 Go 1.21 或更高版本。由旧版升级时，请先阅读 [MIGRATION.md](MIGRATION.md)。

## 核心能力

- TCP、UDP、IPv4、IPv6，以及 TCP TLS。
- 基于 `bufio.SplitFunc` 的流式拆包，默认使用 `bufio.ScanLines`。
- 普通消息回调和基于 action path 的路由两种处理方式。
- 全局中间件、模块中间件、连接过滤器和发送过滤器。
- 并发安全的会话属性、快照遍历、主动发送和关闭。
- 单次、显式的 `Serve`、`Shutdown`、`Close` 生命周期。
- TCP 连接上限、UDP 有界分片队列、帧和数据报大小限制。
- 空闲会话回收、结构化日志、运行时统计和协议错误分类。
- 支持 `context.Context`、TLS 和 IPv6 地址的配套客户端。

## 安装

```bash
go get github.com/zboyco/go-server
```

## 快速开始

默认拆包器是 `bufio.ScanLines`。下面的服务读取一行并原样返回；action 返回 `nil` 时不自动回包。

```go
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	goserver "github.com/zboyco/go-server"
)

func main() {
	server, err := goserver.NewTCP(":8080",
		goserver.WithIdleTimeout(5*time.Minute),
		goserver.WithMaxFrameSize(64*1024),
	)
	if err != nil {
		log.Fatal(err)
	}

	err = server.SetOnMessage(func(ctx context.Context, session *goserver.AppSession, message []byte) ([]byte, error) {
		log.Printf("received from %s: %s", session.RemoteAddr(), message)
		return message, nil
	})
	if err != nil {
		log.Fatal(err)
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-signalCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	if err := server.Serve(); err != nil {
		log.Fatal(err)
	}
}
```

`Serve` 会阻塞，直到监听失败或服务被关闭。服务必须在启动前至少注册一个 `SetOnMessage` action 或命令路由。

## 构造与配置

推荐使用 `NewTCP` 或 `NewUDP`；需要动态选择传输层时使用 `New`。

```go
tcpServer, err := goserver.NewTCP("127.0.0.1:8080")
udpServer, err := goserver.NewUDP("[::1]:8081")
server, err := goserver.New(goserver.TCP, ":8082")
```

地址是标准库接受的完整地址。拼接动态主机和端口时使用 `net.JoinHostPort`，不要手工拼接冒号。

| 选项 | 默认值 | 作用 |
| --- | ---: | --- |
| `WithIdleTimeout` | `5m` | 最大入站空闲时间；`0` 禁用 |
| `WithWriteTimeout` | `30s` | TCP 常规写超时；`0` 禁用 |
| `WithShutdownWriteTimeout` | `1s` | 优雅关闭时 EOF 写入总等待时间 |
| `WithMaxFrameSize` | `64 KiB` | TCP 拆包器接受的最大帧 |
| `WithMaxConnections` | `0` | TCP 活跃处理上限；`0` 不限制 |
| `WithTLS` | 无 | 启用 TCP TLS，配置会被克隆 |
| `WithTLSHandshakeTimeout` | `10s` | TLS 握手超时；`0` 禁用 |
| `WithLogger` | `slog.Default()` | 注入结构化日志器 |
| `WithUDPWorkers` | `GOMAXPROCS` | UDP 有序处理分片数 |
| `WithUDPQueueSize` | `64` | 每个 UDP 分片的有界队列容量 |
| `WithMaxDatagramSize` | `64 KiB` | 接受的最大 UDP 数据报 |
| `WithUDPDispatchMode` | `UDPDispatchQueued` | 在有界并发与最低时延之间选择 |

所有构造参数会立即校验，错误可通过 `errors.Is` 与 `ErrInvalidConfig` 或 `ErrUnsupportedNetwork` 比较。配置方法也会返回错误，并且只能在第一次调用 `Serve` 前执行。

`Option` 只在构造时应用；TLS 配置会被克隆，EOF 字节切片、路由 action 切片和中间件切片会在注册时复制。日志器、回调和过滤函数等对象仍由调用方提供，必须满足并发调用要求；尤其不应在 `Serve` 期间修改过滤器对象自身公开的字段。`WithUDPWorkers` 和 `WithUDPQueueSize` 只影响 queued 模式，inline 模式仍会校验这些构造参数，但不会创建对应队列。

### TLS

TLS 仅支持 TCP：

```go
certificate, err := tls.LoadX509KeyPair("server.crt", "server.key")
if err != nil {
	log.Fatal(err)
}

server, err := goserver.NewTCP(":8443", goserver.WithTLS(&tls.Config{
	MinVersion:   tls.VersionTLS13,
	Certificates: []tls.Certificate{certificate},
}))
```

TLS 握手会在连接过滤器、会话注册和业务回调之前完成，并受 `WithTLSHandshakeTimeout` 与服务器关闭 context 约束。握手失败的连接不会进入会话池，也不会触发新会话回调。

## 生命周期

一个 `Server` 只允许一次监听生命周期：

- `Serve()` 创建监听器并阻塞到 accept/read 循环退出；返回时 cleanup 已启动，但不代表所有用户回调已经排空。
- `Shutdown(ctx)` 停止接收新流量，尝试写入 EOF，并在 context 范围内等待完整排空。
- `Close()` 立即停止接收、中断活跃会话传输，并等待完整排空。
- `RequestShutdown()` 发起优雅停机但不等待，适合在 action 或事件回调中同步调用。
- `RequestClose()` 发起立即关闭但不等待，适合在 action 或事件回调中同步调用。
- `WaitDrained(ctx)` 等待 handler、worker、关闭任务和同步回调全部退出。
- `Addr()` 在监听器绑定后返回实际地址，适用于 `127.0.0.1:0`；停止后返回 `nil`。

`Shutdown` 的调用方 context 控制完整排空的最长等待时间。超时后，库会中断剩余连接，确认 `Serve` 已退出并返回 `ctx.Err()`；忽略 context 的 action、发送过滤器、同步事件回调或永久阻塞的自定义 `slog.Handler` 仍可能继续阻塞 `WaitDrained`。无 context 参数的 `Close()` 同样会等待这些用户代码返回，不提供有限返回保证。启动路由日志在被 drain 跟踪的 worker 中执行，不会阻塞监听循环退出。需要在超时后继续观察最终清理时，可使用新的 context 调用 `WaitDrained`。Action 自行派生的业务 goroutine 不归库所有，调用方仍需自行停止和等待。

`Shutdown`、`Close` 和 `WaitDrained` 会等待 action、连接处理器及 UDP worker 退出，因此不能在这些服务器拥有的 action 或同步事件回调中直接调用。在 action、`OnError`、`OnNewSessionRegister` 或 `OnSessionClosed` 中只调用对应的 `RequestShutdown` 或 `RequestClose`；需要等待最终完成时，由服务器执行路径之外的 goroutine 调用阻塞方法。

可选的 EOF 标记只在优雅关闭时尽力发送：

```go
if err := server.SetEOF([]byte("BYE")); err != nil {
	log.Fatal(err)
}
```

EOF 会经过发送过滤器，不代表对端已经可靠接收。`session.Close(reason)` 和 `server.Close()` 不发送 EOF。

## 消息与路由

### 普通消息

`SetOnMessage` 适合只有一种消息处理路径的协议。回调签名为：

```go
type ActionFunc func(context.Context, *goserver.AppSession, []byte) ([]byte, error)
```

回调返回非 `nil` 字节切片时，服务器同步写回；返回 `nil` 时不回包。context 在会话或服务器开始关闭时取消。

### 命令路由

命令路由需要 `ReceiveFilter` 同时提供拆包器和 action 解析器。内置的开始/结束标记协议示例：

```go
receiveFilter := &filter.BeginEndMarkReceiveFilter{
	Begin: []byte("!$"),
	End:   []byte("$!"),
}

if err := server.SetReceiveFilter(receiveFilter); err != nil {
	log.Fatal(err)
}
if err := server.Action("/echo", func(ctx context.Context, session *goserver.AppSession, payload []byte) ([]byte, error) {
	return payload, nil
}); err != nil {
	log.Fatal(err)
}

// 接收过滤器不负责封装响应；需要相同外壳时显式注册发送过滤器。
if err := server.RegisterSendPacketFilter(goserver.Middlewares{
	func(ctx context.Context, session *goserver.AppSession, payload []byte) ([]byte, error) {
		packet := make([]byte, 0, len(receiveFilter.Begin)+len(payload)+len(receiveFilter.End))
		packet = append(packet, receiveFilter.Begin...)
		packet = append(packet, payload...)
		packet = append(packet, receiveFilter.End...)
		return packet, nil
	},
}); err != nil {
	log.Fatal(err)
}
```

`BeginEndMarkReceiveFilter` 的标记内部格式为：4 字节大端 action 长度、action、payload。`FixedHeaderReceiveFilter` 的格式为：4 字节大端总长度、4 字节大端 action 长度、action、payload；总长度包含 8 字节头。

路由必须以 `/` 开头，匹配时不区分大小写。重复路由返回 `ErrActionConflict`，不存在的路由返回 `ErrActionNotFound`。

action 收到的 `payload` 可能直接引用 TCP Scanner 或 UDP 复用缓冲区，只保证在当前同步 action/中间件调用链返回前有效。需要异步处理、延迟保存或跨回调持有时，必须先复制数据，例如 `payload = bytes.Clone(payload)`。action 返回的响应会在调用链内同步完成过滤和写入。

### 模块路由

模块会注册所有符合 `ActionFunc` 签名的导出方法：

```go
type module struct{}

func (*module) Root() string { return "/v1" }

func (*module) Ping(ctx context.Context, session *goserver.AppSession, payload []byte) ([]byte, error) {
	return []byte("pong"), nil
}

if err := server.RegisterModule(&module{}); err != nil {
	log.Fatal(err)
}
```

上例注册 `/v1/ping`。模块还可以实现 `ActionSummary`、`MiddlewaresBeforeAction` 或 `MiddlewaresAfterAction`。

### 中间件顺序

完整执行顺序为：

```text
全局 before -> 模块 before -> action -> 模块 after -> 全局 after -> 发送过滤器
```

任一步返回错误都会停止后续处理。最终结果为 `nil` 时不会发送响应。`RegisterBeforeMiddlewares`、`RegisterAfterMiddlewares` 和 `RegisterSendPacketFilter` 会替换相应的完整中间件集合。

显式路由的 action 切片和所有中间件切片都会在注册时复制，调用方后续增删原切片不会影响服务端。nil action 或 nil 中间件会在注册时返回 `ErrInvalidConfig`；模块路由会先检查全部冲突，再一次性提交，不会留下部分注册结果。

## 会话

`AppSession` 代表一条 TCP 连接或一个 UDP 远端 endpoint。常用方法如下：

```go
session.ID
session.Context()
session.Network()
session.RemoteAddr()
session.Closed()
session.Send(ctx, data)       // 经过发送过滤器
session.SendRaw(ctx, data)    // 跳过发送过滤器
session.Close("reason")
```

同一会话的写入会串行化。`Send` 和 `SendRaw` 会完整写入数据或返回错误；context 可用于取消和截止时间。会话关闭是幂等的，`Closed()` 从会话进入 closing 起即返回 `true`，关闭回调此时可能仍在排空。

会话属性方法 `AddAttr`、`SetAttr`、`GetAttr`、`DelAttr` 支持并发访问。服务端查询返回稳定快照，不会持有内部锁等待调用方：

```go
all := server.Sessions()
admins := server.FindSessions(func(attributes map[string]any) bool {
	role, _ := attributes["role"].(string)
	return role == "admin"
})
count := server.CountSessions()
session, err := server.GetSessionByID(id)
```

属性 map 使用浅拷贝快照：调用方修改 `FindSessions` 收到的 map 不会影响会话，但 map、slice、指针等属性值指向的对象不会深拷贝，其内部并发安全仍由业务负责。`Sessions`、`FindSessions`、`CountSessions` 和 `GetSessionByID` 只观察开放会话；查询后会话仍可能并发开始关闭。

## UDP 语义与过载

UDP 严格保留数据报边界：

- 一个数据报可以包含一个或多个完整帧，但不会与前后数据报拼接。
- 数据报结束时仍不完整的帧会返回协议错误，并关闭对应的 UDP 会话。
- 自定义 `bufio.SplitFunc` 返回 `bufio.ErrFinalToken` 时，会分发随附的最后一个 token 并正常结束当前数据报。
- 空数据报作为零长度 token 分发；配置了路由解析器时仍须通过对应协议校验。
- 同一规范化远端地址复用一个会话。
- 超过 `WithMaxDatagramSize` 的数据报会被丢弃，并报告 `ErrDatagramTooLarge`。

默认的 `UDPDispatchQueued` 使用有界有序分片：

- 同一 endpoint 始终路由到同一 worker，从而保持处理顺序。
- 不同分片可以并行处理；不承诺不同 endpoint 之间的全局顺序。
- action 不会阻塞 socket 读取循环。
- 分片队列已满时丢弃新数据报，增加 `PacketsDropped`，并通过 `OnError` 报告 `ErrUDPQueueFull`。
- `WithUDPWorkers` 和 `WithUDPQueueSize` 只影响 queued 模式。

低时延、短 action 场景可以显式选择 `UDPDispatchInline`：

```go
server, err := goserver.NewUDP(":8080",
	goserver.WithUDPDispatchMode(goserver.UDPDispatchInline),
	goserver.WithIdleTimeout(30*time.Second),
)
```

inline 模式使用一个独立 worker 与 socket 读取循环逐包交接，不使用有界排队或逐包缓冲池。读取循环等待当前数据报处理完成后才读取下一包，因此仍保持全局顺序，慢 action 也仍会阻塞后续读取；但 shutdown 可以取消读取循环的等待，使 `Serve` 不被用户 action 卡住。需要跨 endpoint 并发或显式用户态背压时，应保留默认 queued 模式。

UDP 本身不提供可靠送达。需要可靠性时，应在应用协议实现确认、重试和幂等。

## 连接过滤与事件

连接过滤器在创建会话前执行：

```go
server.RegisterConnectionFilterTCP(func(conn net.Conn) error { return nil })
server.RegisterConnectionFilterUDP(func(addr *net.UDPAddr) error { return nil })
```

返回错误会拒绝连接或 endpoint。可注册以下事件：

```go
server.SetOnError(func(err error) {})
server.SetOnNewSessionRegister(func(session *goserver.AppSession) {})
server.SetOnSessionClosed(func(session *goserver.AppSession, reason string) {})
```

TCP 过滤器在 TLS 握手成功后、会话注册前执行；UDP 过滤器在首次发现 endpoint、会话注册前执行。过滤器返回错误会拒绝连接或丢弃当前数据报，nil 过滤器条目会被忽略。

生命周期事件和错误回调中的 panic 会被隔离并记录。业务仍应保证 action、过滤器和中间件不 panic，并通过返回错误表达失败。`reason` 是关闭路径提供的诊断文本，不应当作稳定的机器协议字段。

常用可判定错误包括：

```go
goserver.ErrServerRunning
goserver.ErrServerClosed
goserver.ErrInvalidConfig
goserver.ErrUnsupportedNetwork
goserver.ErrSessionClosed
goserver.ErrSessionNotFound
goserver.ErrUDPQueueFull
goserver.ErrDatagramTooLarge
goserver.ErrInvalidFrame
goserver.ErrPathFormat
goserver.ErrActionNotFound
goserver.ErrActionConflict
goserver.ErrModuleFormat
```

协议错误位于 `filter` 包：`ErrInvalidMarkLength`、`ErrInvalidPackageLength`、`ErrInvalidTokenLength`、`ErrInvalidActionNameLength`。内部错误可能带上下文包装，使用 `errors.Is` 判断。

## 运行时统计

`server.Stats()` 返回无锁的瞬时累计值。各字段独立原子读取，适合监控，但不是跨字段事务快照：

```go
stats := server.Stats()
log.Printf("accepted=%d active=%d read=%d written=%d dropped=%d protocol_errors=%d",
	stats.AcceptedConnections,
	server.CountSessions(),
	stats.BytesRead,
	stats.BytesWritten,
	stats.PacketsDropped,
	stats.ProtocolErrors,
)
```

| 字段 | 语义 |
| --- | --- |
| `AcceptedConnections` | 通过握手、连接过滤和服务状态校验后成功准入的 TCP/UDP 会话数 |
| `ClosedSessions` | 已完成最终关闭流程的 `AppSession` 数量 |
| `BytesRead` | TCP 拆包后的 token 字节数，通常不含分隔符；UDP 从 socket 读取的完整数据报字节数，包括随后因超限丢弃的数据报 |
| `BytesWritten` | 底层传输实际成功写出的字节数 |
| `PacketsRead` | 从 UDP socket 成功读出的数据报数；TCP 服务为 `0` |
| `PacketsDropped` | 因最大数据报限制或 queued 队列满而由用户态主动丢弃的 UDP 包数，不包含不可见的内核丢包 |
| `ProtocolErrors` | 明确归类的协议解析错误；UDP 包含拆包和路由解析错误，TCP 当前累计路由解析错误 |

所有计数器在一次 Server 生命周期内累计，不会自动按时间窗口归零。`CountSessions()` 是当前值，不属于累计统计。

## 客户端

`client.SimpleClient` 支持 TCP/UDP、context、超时、TLS 和可选 `bufio.SplitFunc` 拆包：

```go
cli, err := client.NewSimpleClient(goserver.TCP, "127.0.0.1:8080",
	client.WithDialTimeout(3*time.Second),
	client.WithReadTimeout(3*time.Second),
	client.WithWriteTimeout(3*time.Second),
)
if err != nil {
	log.Fatal(err)
}
if err := cli.Connect(ctx); err != nil {
	log.Fatal(err)
}
defer cli.Close()

if err := cli.Send(ctx, []byte("hello\n")); err != nil {
	log.Fatal(err)
}
response, err := cli.Receive(ctx)
```

| 客户端选项 | 默认值 | 作用 |
| --- | ---: | --- |
| `client.WithBufferSize` | `4 KiB` | TCP 原始读取大小及 framing 初始缓冲；不能超过最大帧 |
| `client.WithMaxScanTokenSize` | `64 KiB` | TCP framing 接受的最大响应帧 |
| `client.WithScannerSplitFunc` | 无 | 启用 TCP/UDP framing；函数不能为 nil |
| `client.WithTLS` | 无 | 为 TCP 启用 TLS，配置会被克隆 |
| `client.WithDialTimeout` | `30s` | 建立连接的默认超时；`0` 禁用 |
| `client.WithReadTimeout` | `30s` | `Receive` 的默认超时；`0` 禁用 |
| `client.WithWriteTimeout` | `30s` | `Send` 的默认超时；`0` 禁用 |
| `client.WithMaxDatagramSize` | `64 KiB` | 客户端接受的最大 UDP 响应数据报 |

每次操作会取调用方 context 截止时间与选项默认超时中的较早者；主动取消 context 也会立即解除阻塞的网络读写。`Connect(nil)`、`Send(nil, ...)` 和 `Receive(nil)` 使用 `context.Background()`。

尚未连接或 `Close` 后调用 `Send`、`Receive` 会返回 `client.ErrNotConnected`；该错误不再匹配服务端会话错误 `goserver.ErrSessionClosed`。

TCP framing 会保留未完成帧；一次 `Receive` 因 context 或读取超时返回后，可以使用新 context 继续读取同一帧，不需要重连。

UDP 始终按单个数据报解析，不会跨数据报拼包。同一数据报包含多个完整帧时，后续 `Receive` 会依次返回缓存帧。默认接受最大 64 KiB UDP 响应，可通过 `client.WithMaxDatagramSize` 降低上限；超限返回 `goserver.ErrDatagramTooLarge`，不会静默截断。客户端为每个实例复用一个 `maxDatagramSize+1` 字节的接收缓冲，降低稳态分配的代价是该内存在首次 UDP `Receive` 后会随客户端实例保持可达；大批长生命周期客户端应按协议实际上限调低配置。

命令路由可使用 `NewBeginEndMarkClient` 和 `SendAction`。`Connect` 只在尚未连接时建立新连接，`Close` 会清除所有连接级 framing 状态。客户端实例的读、写和生命周期操作分别串行化；同一实例可以一个 goroutine 读、另一个 goroutine 写，但不应依赖多个并发 `Receive` 或多个并发 `Send` 的完成顺序。`Conn()` 只提供底层连接快照，业务不应并发修改其 deadline，否则会破坏客户端的 context 和超时语义。

## 验证与性能

```bash
go test ./...
go test -race ./...
go vet ./...
go test -run '^$' -bench . -benchmem
go doc -all .
go doc -all ./client
go doc -all ./filter
```

仓库包含 TCP/UDP 往返、UDP 串行/并行、慢 action、队列饱和、核心处理和长连接分配基准。性能结果受操作系统、CPU 调度、GOMAXPROCS 和监听地址影响；做版本比较时，应在同一机器上多轮运行，并使用 `benchstat` 比较原始结果。

不要把其他机器或历史版本的数字作为当前性能承诺。当前实现控制无界并发：会话存储没有后台生产 goroutine，TCP 自动响应路径可避免额外取消 watcher，queued UDP 使用复用缓冲和固定容量分片队列，inline UDP 使用单一复用读取缓冲及复用 ack。每次生命周期或调度改动后应重新采集原始结果。

## 完整示例

`example` 目录覆盖 TCP、UDP、TLS、IPv6、内置协议、自定义拆包、自定义路由过滤器、中间件、会话、空闲超时和停机流程：

```bash
go run ./example
```
