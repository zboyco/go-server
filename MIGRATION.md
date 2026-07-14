# v1 迁移指南

v1 是一次直接的破坏性升级，不提供旧 API 兼容层。迁移前先升级到 Go 1.21 或更高版本，并在独立分支完成编译、竞态测试和真实协议回归。

## API 对照

| 旧版 | v1 | 说明 |
| --- | --- | --- |
| `New(network, ip, port)` | `New(network, address, opts...)` | 传入完整地址并处理 `(*Server, error)` |
| `NewTCP(ip, port)` | `NewTCP(address, opts...)` | 使用 `net.JoinHostPort` 安全处理 IPv6 |
| `NewUDP(ip, port)` | `NewUDP(address, opts...)` | 构造阶段立即校验配置 |
| `NewTCPWithTLS(...)` | `NewTCP(address, WithTLS(config))` | TLS 只允许用于 TCP |
| `Start()` | `Serve() error` | 阻塞运行并返回监听/服务错误 |
| `Stop()` | `Close() error` | 立即关闭；优雅停机使用 `Shutdown(ctx)` |
| 无 | `WaitDrained(ctx)` | 在 `Serve` 返回后继续等待完整清理 |
| `IsClosed` 字段 | `Closed() bool` | 使用原子状态快照 |
| `IdleSessionTimeOut` | `WithIdleTimeout(duration)` | 从秒数改为 `time.Duration` |
| `SetMaxScanTokenSize(n)` | `WithMaxFrameSize(n)` | 在构造时设置 |
| `GetAllSessions()` | `Sessions()` | 从 channel 改为稳定切片快照 |
| `GetSessionByAttr(cond)` | `FindSessions(cond)` | 从 channel 改为稳定切片快照 |
| `AcceptCount` | `Stats().AcceptedConnections` | 改为握手和过滤后成功准入的会话数；容量用 `WithMaxConnections` |
| `SafeByteSlice` | `[]byte` | 类型已删除；跨 goroutine 共享时由业务显式复制或同步 |

## 构造函数

旧版将 IP 和端口分开传入，且通常不返回错误：

```go
server := goserver.New(goserver.TCP, host, port)
```

v1 使用标准完整地址和函数式选项：

```go
address := net.JoinHostPort(host, strconv.Itoa(port))
server, err := goserver.NewTCP(address,
	goserver.WithIdleTimeout(5*time.Minute),
	goserver.WithMaxFrameSize(128*1024),
)
if err != nil {
	return err
}
```

空地址现在是无效配置。监听所有本地地址时使用 `":8080"` 或 `"[::]:8080"`。

TLS 握手现在必须在会话注册前完成，默认握手超时为 10 秒。需要调整时使用 `WithTLSHandshakeTimeout`；非法 TLS 或 mTLS 校验失败的连接不会触发会话回调。

## Action 和中间件

所有 action、中间件和发送过滤器新增第一个 `context.Context` 参数：

```go
// 旧版
func(session *goserver.AppSession, payload []byte) ([]byte, error)

// v1
func(ctx context.Context, session *goserver.AppSession, payload []byte) ([]byte, error)
```

该 context 与会话生命周期绑定，服务器开始关闭时会取消。阻塞 I/O 和业务调用应继续向下传递它。返回非 `nil` 数据仍会自动发送，返回 `nil` 表示不响应。

模块反射方法也必须改为完整的新签名，否则 v1 会忽略该方法：

```go
func (*module) Ping(ctx context.Context, session *goserver.AppSession, payload []byte) ([]byte, error)
```

所有配置和注册方法现在返回 `error`，调用方不能再忽略失败：

```go
if err := server.SetOnMessage(handler); err != nil {
	return err
}
if err := server.RegisterModule(&module{}); err != nil {
	return err
}
```

配置冻结在 `Serve` 开始时。运行期间再次设置回调、过滤器、路由或中间件会返回 `ErrServerRunning`。

## 生命周期

```go
serveErr := make(chan error, 1)
go func() {
	serveErr <- server.Serve()
}()

ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := server.Shutdown(ctx); err != nil {
	return err
}
if err := <-serveErr; err != nil {
	return err
}
```

一个 `Server` 只对应一次生命周期。停止后需要重新监听时，重新构造实例。`Serve` 在监听循环退出且 cleanup 启动后返回，不再表示所有 handler 已排空。`Shutdown` 会尝试发送配置的 EOF 并等待完整排空；context 到期后中断剩余传输、保证 `Serve` 已退出并返回 context 错误。需要继续等待时调用 `WaitDrained`。`Close` 始终执行立即关闭并等待完整排空。

`Shutdown`、`Close` 和 `WaitDrained` 都会等待库管理的 action、连接处理器、UDP worker 和启动日志任务，因此不要从 action 或同步事件回调中直接调用。忽略 context 的 action、发送过滤器、同步事件回调以及阻塞的自定义 `slog.Handler` 不会阻止 `Serve` 退出，但会延迟完整排空；无 context 参数的 `Close` 也可能一直等待它们返回。需要在这些执行路径中触发停止时，改用立即返回的 `RequestShutdown` 或 `RequestClose`；需要等待完整排空时，再由服务器执行路径之外的 goroutine 调用阻塞方法。

迁移时特别检查：

- 不要再把 `Serve` 放入无人接收错误的 goroutine。
- 不要在 `Serve` 返回后复用同一个 Server。
- 不要把 `Shutdown` 当作业务任务排空器；业务自行创建的 goroutine 仍由业务等待。
- 需要可靠的协议结束确认时，在应用层实现 ACK，不要依赖尽力发送的 EOF。

## Session

发送方法新增 context，关闭方法新增错误返回：

```go
ctx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()

if err := session.Send(ctx, payload); err != nil {
	return err
}
if err := session.SendRaw(ctx, payload); err != nil {
	return err
}
if err := session.Close("completed"); err != nil {
	return err
}
```

`Send` 经过发送过滤器，`SendRaw` 跳过过滤器。两者会串行化同一会话上的写操作，并完整写入或返回错误。关闭后发送返回 `ErrSessionClosed`。

使用 `session.Context()`、`Network()` 和 `RemoteAddr()` 代替读取底层实现状态。`Closed()` 从 closing 起返回 `true`，但不代表关闭回调已经完成；业务同步仍应依赖 context、发送错误、关闭回调或服务级 `WaitDrained`。

旧版 channel 遍历改为切片快照：

```go
for _, session := range server.Sessions() {
	_ = session.Send(ctx, notification)
}

matched := server.FindSessions(func(attributes map[string]any) bool {
	return attributes["tenant"] == tenantID
})
```

`FindSessions` 收到的是属性 map 的浅拷贝。map 本身与会话隔离，但其中的引用类型值仍由业务负责并发安全。

## UDP 行为变化

v1 不再跨 UDP 数据报拼包。每个数据报必须包含完整帧；数据报末尾的半帧会作为 `ErrInvalidFrame` 或具体过滤器错误处理，并关闭该远端会话。

默认处理模型改为固定数量的有界有序分片：

- 同一远端 endpoint 在同一分片内顺序处理。
- 不同分片可以并行。
- 队列已满时新数据报会被丢弃，并报告 `ErrUDPQueueFull`。
- 超过最大数据报配置时会被丢弃，并报告 `ErrDatagramTooLarge`。
- 自定义 `bufio.SplitFunc` 的 `bufio.ErrFinalToken` 会按标准 Scanner 语义提交最后一个 token。
- 空数据报会作为零长度 token 交给普通 action；路由协议仍会执行自己的 token 校验。
- UDP 空闲回收由服务级 sweeper 统一完成，不再为每个会话维护等待 goroutine。

默认模式是 `UDPDispatchQueued`。迁移前应确认客户端不会把一帧拆成多个 UDP 数据报，并为业务允许的突发量配置 `WithUDPWorkers`、`WithUDPQueueSize`、`WithMaxDatagramSize`。

纯低时延、短 action 场景可显式启用 inline：

```go
server, err := goserver.NewUDP(address,
	goserver.WithUDPDispatchMode(goserver.UDPDispatchInline),
)
```

`UDPDispatchInline` 不使用有界用户态队列，数据报通过单 worker 与 socket 读取循环逐包交接。慢 action 仍会阻塞后续读取，但 shutdown 可以取消读取循环等待，使 `Serve` 先退出；完整排空仍会等待 action 返回。`WithUDPWorkers` 和 `WithUDPQueueSize` 不影响 inline action 并发度。

## 客户端

客户端构造函数现在接收完整地址并返回错误，连接、收发均接收 context：

```go
cli, err := client.NewSimpleClient(goserver.TCP, address,
	client.WithDialTimeout(3*time.Second),
	client.WithReadTimeout(3*time.Second),
	client.WithWriteTimeout(3*time.Second),
)
if err != nil {
	return err
}
if err := cli.Connect(ctx); err != nil {
	return err
}
defer cli.Close()

if err := cli.Send(ctx, request); err != nil {
	return err
}
response, err := cli.Receive(ctx)
```

TCP framing 在读取超时后会保留半帧，下一次 `Receive` 可使用新 context 继续。UDP framing 严格限制在单个数据报内，不再跨数据报拼接；同一数据报中的多个完整帧会由后续 `Receive` 依次返回。UDP 客户端默认最大数据报为 64 KiB，可用 `client.WithMaxDatagramSize` 调低，超限返回 `goserver.ErrDatagramTooLarge`。

尚未连接或已经 `Close` 的客户端调用 `Send`、`Receive` 时返回 `client.ErrNotConnected`，不再复用 `goserver.ErrSessionClosed`。

原先依赖字段拼接的 IPv6 地址应统一改用 `net.JoinHostPort`。TLS 改用 `client.WithTLS`，并且只能与 TCP 一起使用。

## 错误和可观测性

v1 导出稳定的 sentinel errors。错误通常会通过 `%w` 包装上下文，因此使用 `errors.Is`：

```go
if errors.Is(err, goserver.ErrSessionClosed) {
	// 会话已关闭。
}
if errors.Is(err, goserver.ErrSessionNotFound) {
	// 当前没有对应的开放会话。
}
if errors.Is(err, filter.ErrInvalidPackageLength) {
	// 对端发送了非法协议帧。
}
```

通过 `WithLogger` 注入 `*slog.Logger`，通过 `SetOnError` 接收运行时错误，通过 `Stats()` 采集连接、字节、UDP 丢包和协议错误计数。`AcceptedConnections` 现在只统计握手、连接过滤和服务状态校验后成功准入的 TCP/UDP 会话；高频 UDP 队列溢出不会逐包写日志，监控应同时观察 `PacketsDropped`。

## 建议迁移顺序

1. 将 Go 工具链升级到 1.21 或更高版本。
2. 迁移构造函数和完整监听地址，处理所有返回错误。
3. 为 action、模块方法和中间件增加 `context.Context`。
4. 将 `Start/Stop` 替换为 `Serve/Shutdown/Close`，接收 `Serve` 错误。
5. 将会话字段和 channel 遍历迁移到新方法及快照 API。
6. 迁移客户端构造、连接、发送和接收调用。
7. 对 UDP 协议执行数据报边界和过载测试。
8. 运行 `go test ./...`、`go test -race ./...` 和真实协议集成测试。
9. 在同一环境使用现有基准与生产帧大小做迁移前后对比。
