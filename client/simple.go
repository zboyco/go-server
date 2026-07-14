package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	goserver "github.com/zboyco/go-server/v2"
)

// Option 描述一个 SimpleClient 构造选项。
type Option interface {
	apply(*clientConfig) error
}

// optionFunc 将普通配置函数适配为 Option。
type optionFunc func(*clientConfig) error

// apply 把当前选项应用到待校验的客户端配置。
func (fn optionFunc) apply(config *clientConfig) error {
	return fn(config)
}

// clientConfig 保存 SimpleClient 的不可变构造配置。
type clientConfig struct {
	bufferSize       int
	maxScanTokenSize int
	split            bufio.SplitFunc
	tlsConfig        *tls.Config
	dialTimeout      time.Duration
	readTimeout      time.Duration
	writeTimeout     time.Duration
	maxDatagramSize  int
}

// SimpleClient 是支持 context、超时和可选拆包的 TCP/UDP 客户端。
//
// 生命周期、读取和写入分别串行化，因此同一实例可以由一个 goroutine 读取、另一个 goroutine 写入；
// 多个并发 Receive 或多个并发 Send 会依次执行，不应依赖它们竞争锁后的完成顺序。
type SimpleClient struct {
	network goserver.Network
	address string
	config  clientConfig

	lifecycleMu sync.RWMutex
	readMu      sync.Mutex
	writeMu     sync.Mutex
	conn        net.Conn
	framer      *frameReader
	udpFrames   [][]byte
	// udpReadBuffer 受 readMu 保护并跨 UDP Receive 复用，以减少稳态分配。
	udpReadBuffer []byte
}

// NewSimpleClient 创建并校验连接 address 的 TCP 或 UDP 客户端。
//
// 构造函数不建立网络连接，调用方需显式调用 Connect。network 仅支持 goserver.TCP 或
// goserver.UDP；TLS 仅支持 TCP。任一 option 为 nil 或最终字段约束不合法时返回错误。
func NewSimpleClient(network goserver.Network, address string, options ...Option) (*SimpleClient, error) {
	config := clientConfig{
		bufferSize:       4 * 1024,
		maxScanTokenSize: bufio.MaxScanTokenSize,
		dialTimeout:      30 * time.Second,
		readTimeout:      30 * time.Second,
		writeTimeout:     30 * time.Second,
		maxDatagramSize:  64 * 1024,
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("client option must not be nil")
		}
		if err := option.apply(&config); err != nil {
			return nil, err
		}
	}
	if network != goserver.TCP && network != goserver.UDP {
		return nil, fmt.Errorf("%w: %q", goserver.ErrUnsupportedNetwork, network)
	}
	if address == "" {
		return nil, fmt.Errorf("client address must not be empty")
	}
	if config.bufferSize <= 0 || config.maxScanTokenSize <= 0 || config.bufferSize > config.maxScanTokenSize {
		return nil, fmt.Errorf("invalid client buffer sizes")
	}
	if config.dialTimeout < 0 || config.readTimeout < 0 || config.writeTimeout < 0 {
		return nil, fmt.Errorf("client timeouts must not be negative")
	}
	if config.maxDatagramSize <= 0 || config.maxDatagramSize > 64*1024 {
		return nil, fmt.Errorf("client max datagram size must be between 1 and 65536")
	}
	if config.tlsConfig != nil && network != goserver.TCP {
		return nil, fmt.Errorf("TLS client requires TCP")
	}
	return &SimpleClient{network: network, address: address, config: config}, nil
}

// WithBufferSize 设置 TCP 原始 Receive 的单次缓冲大小，以及 framing 模式的初始缓冲容量。
// size 必须大于 0 且不能超过 WithMaxScanTokenSize。
func WithBufferSize(size int) Option {
	return optionFunc(func(config *clientConfig) error {
		config.bufferSize = size
		return nil
	})
}

// WithMaxScanTokenSize 设置客户端接受的最大 TCP framing 响应大小，单位为字节。
func WithMaxScanTokenSize(size int) Option {
	return optionFunc(func(config *clientConfig) error {
		config.maxScanTokenSize = size
		return nil
	})
}

// WithScannerSplitFunc 设置非 nil 的 bufio.SplitFunc，并使 Receive 每次返回一个完整帧。
// TCP 会跨读取保留未完成帧；UDP 仅在单个数据报内部拆包。
func WithScannerSplitFunc(split bufio.SplitFunc) Option {
	return optionFunc(func(config *clientConfig) error {
		if split == nil {
			return fmt.Errorf("client split function must not be nil")
		}
		config.split = split
		return nil
	})
}

// WithTLS 为 TCP 客户端启用 TLS，并克隆传入配置以隔离调用方后续修改。
// tlsConfig 不能为 nil，且与 UDP 一同使用会在构造校验时返回错误。
func WithTLS(tlsConfig *tls.Config) Option {
	return optionFunc(func(config *clientConfig) error {
		if tlsConfig == nil {
			return fmt.Errorf("client TLS config must not be nil")
		}
		config.tlsConfig = tlsConfig.Clone()
		return nil
	})
}

// WithDialTimeout 设置建立连接的默认超时，0 表示不设置默认超时。
// Connect 的 context 若具有更早截止时间，仍由 net.Dialer 优先响应。
func WithDialTimeout(timeout time.Duration) Option {
	return optionFunc(func(config *clientConfig) error {
		config.dialTimeout = timeout
		return nil
	})
}

// WithReadTimeout 设置 Receive 的默认超时，0 表示不设置默认读截止时间。
// 调用方 context 中更早的截止时间具有优先级。
func WithReadTimeout(timeout time.Duration) Option {
	return optionFunc(func(config *clientConfig) error {
		config.readTimeout = timeout
		return nil
	})
}

// WithWriteTimeout 设置 Send 的默认超时，0 表示不设置默认写截止时间。
// 调用方 context 中更早的截止时间具有优先级。
func WithWriteTimeout(timeout time.Duration) Option {
	return optionFunc(func(config *clientConfig) error {
		config.writeTimeout = timeout
		return nil
	})
}

// WithMaxDatagramSize 设置客户端接受的最大 UDP 响应数据报，合法范围为 1 到 65536 字节。
// 客户端会多读一个字节以可靠识别超限数据报，超限时返回 goserver.ErrDatagramTooLarge。
func WithMaxDatagramSize(size int) Option {
	return optionFunc(func(config *clientConfig) error {
		config.maxDatagramSize = size
		return nil
	})
}

// Connect 建立连接，并重置所有与旧连接绑定的拆包状态。
//
// ctx 为 nil 时使用 context.Background。已连接时直接返回 nil；失败时不保存半成品连接。
// TLS 握手由 tls.Dialer 在本次调用内完成。
func (client *SimpleClient) Connect(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	client.lifecycleMu.Lock()
	defer client.lifecycleMu.Unlock()
	if client.conn != nil {
		return nil
	}
	dialer := &net.Dialer{Timeout: client.config.dialTimeout}
	var (
		conn net.Conn
		err  error
	)
	if client.config.tlsConfig != nil {
		tlsDialer := &tls.Dialer{NetDialer: dialer, Config: client.config.tlsConfig}
		conn, err = tlsDialer.DialContext(ctx, string(client.network), client.address)
	} else {
		conn, err = dialer.DialContext(ctx, string(client.network), client.address)
	}
	if err != nil {
		return fmt.Errorf("dial %s %s: %w", client.network, client.address, err)
	}
	client.conn = conn
	client.framer = nil
	client.udpFrames = nil
	return nil
}

// Conn 返回当前底层连接快照，尚未连接或已关闭时返回 nil。
// 调用方不应直接并发修改连接截止时间，否则会干扰 Send 和 Receive 的 context 语义。
func (client *SimpleClient) Conn() net.Conn {
	client.lifecycleMu.RLock()
	defer client.lifecycleMu.RUnlock()
	return client.conn
}

// Close 断开当前连接，并清除 TCP 残留帧和 UDP 已拆分帧等连接级状态。
// 未连接时返回 nil；该方法不会自动重新连接。
func (client *SimpleClient) Close() error {
	client.lifecycleMu.Lock()
	conn := client.conn
	client.conn = nil
	client.framer = nil
	client.udpFrames = nil
	client.lifecycleMu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// Send 写完全部 content，或返回 context/网络错误。
//
// 同一客户端的并发发送会串行化。TCP 通过循环处理短写；UDP 的底层 Write 对应一个数据报，
// 因而调用方应保证 content 不超过目标网络允许的数据报大小。
func (client *SimpleClient) Send(ctx context.Context, content []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	conn := client.Conn()
	if conn == nil {
		return ErrNotConnected
	}
	reset, err := applyDeadline(ctx, conn.SetWriteDeadline, client.config.writeTimeout)
	if err != nil {
		return err
	}
	defer reset()
	for len(content) > 0 {
		written, writeErr := conn.Write(content)
		if written > 0 {
			content = content[written:]
		}
		if writeErr != nil {
			return normalizeContextError(ctx, writeErr)
		}
		if written == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

// Receive 读取一个原始 TCP 数据块、一个原始 UDP 数据报，或一个由 SplitFunc 解析的完整帧。
//
// TCP framing 会在超时等暂时错误后保留未完成字节，下次可使用新 context 继续读取；
// UDP framing 严格限制在单个数据报内，同一包的后续帧会缓存给之后的 Receive。
// 返回字节切片由调用方独占，可在下一次 Receive 后继续使用。
func (client *SimpleClient) Receive(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client.readMu.Lock()
	defer client.readMu.Unlock()

	client.lifecycleMu.Lock()
	conn := client.conn
	if conn == nil {
		client.lifecycleMu.Unlock()
		return nil, ErrNotConnected
	}
	if client.network == goserver.UDP && len(client.udpFrames) > 0 {
		frame := client.udpFrames[0]
		client.udpFrames[0] = nil
		client.udpFrames = client.udpFrames[1:]
		client.lifecycleMu.Unlock()
		return frame, nil
	}
	if client.network == goserver.TCP && client.config.split != nil && client.framer == nil {
		client.framer = newFrameReader(client.config.split, client.config.bufferSize, client.config.maxScanTokenSize)
	}
	framer := client.framer
	network := client.network
	split := client.config.split
	maxDatagramSize := client.config.maxDatagramSize
	client.lifecycleMu.Unlock()

	reset, err := applyDeadline(ctx, conn.SetReadDeadline, client.config.readTimeout)
	if err != nil {
		return nil, err
	}
	defer reset()
	if network == goserver.UDP {
		if len(client.udpReadBuffer) != maxDatagramSize+1 {
			client.udpReadBuffer = make([]byte, maxDatagramSize+1)
		}
		buffer := client.udpReadBuffer
		size, readErr := conn.Read(buffer)
		if readErr != nil {
			return nil, normalizeContextError(ctx, readErr)
		}
		if size > maxDatagramSize {
			return nil, fmt.Errorf("%w: got at least %d bytes, max %d", goserver.ErrDatagramTooLarge, size, maxDatagramSize)
		}
		if split == nil {
			response := make([]byte, size)
			copy(response, buffer[:size])
			return response, nil
		}
		frames, splitErr := splitUDPDatagram(buffer[:size], split)
		if splitErr != nil {
			return nil, splitErr
		}
		if len(frames) > 1 {
			client.lifecycleMu.Lock()
			if client.conn == conn {
				client.udpFrames = append(client.udpFrames, frames[1:]...)
			}
			client.lifecycleMu.Unlock()
		}
		return frames[0], nil
	}
	if framer == nil {
		buffer := make([]byte, client.config.bufferSize)
		size, readErr := conn.Read(buffer)
		if readErr != nil {
			return nil, normalizeContextError(ctx, readErr)
		}
		return buffer[:size], nil
	}
	frame, frameErr := framer.Read(conn)
	if frameErr != nil {
		return nil, normalizeContextError(ctx, frameErr)
	}
	return frame, nil
}

// normalizeContextError 在 context 已结束时优先返回其错误，而不是底层为解除阻塞产生的 I/O 错误。
func normalizeContextError(ctx context.Context, operationErr error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if networkErr, ok := operationErr.(net.Error); ok && networkErr.Timeout() {
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
	}
	return operationErr
}

// applyDeadline 把 context 的截止和取消语义组合到连接截止时间上，并与默认超时取较早者。
// 返回的清理函数必须调用，以等待可能已触发的取消回调并清除截止时间，避免污染后续操作。
func applyDeadline(ctx context.Context, setDeadline func(time.Time) error, fallback time.Duration) (func(), error) {
	var deadline time.Time
	if fallback > 0 {
		deadline = time.Now().Add(fallback)
	}
	if contextDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || contextDeadline.Before(deadline)) {
		deadline = contextDeadline
	}
	if !deadline.IsZero() {
		if err := setDeadline(deadline); err != nil {
			return nil, err
		}
	}
	stopCancel := func() bool { return true }
	var cancelCallbackDone <-chan struct{}
	if ctx.Done() != nil {
		done := make(chan struct{})
		cancelCallbackDone = done
		stopCancel = context.AfterFunc(ctx, func() {
			defer close(done)
			_ = setDeadline(time.Now())
		})
	}
	return func() {
		if !stopCancel() && cancelCallbackDone != nil {
			<-cancelCallbackDone
		}
		_ = setDeadline(time.Time{})
	}, nil
}
