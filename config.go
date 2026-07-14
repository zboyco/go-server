package goserver

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log/slog"
	"runtime"
	"time"
)

// UDPDispatchMode 表示 UDP 数据报的调度模式，用于在处理隔离与固定时延之间取舍。
type UDPDispatchMode uint8

const (
	// UDPDispatchQueued 使用有界、有序的 worker 分片队列。
	//
	// 同一远端地址始终进入同一分片，因此能保持该地址的数据报处理顺序；
	// action 在 worker 中执行，不会直接阻塞 socket 读取循环。队列已满时新数据报会被丢弃。
	UDPDispatchQueued UDPDispatchMode = iota
	// UDPDispatchInline 在 socket 读取 goroutine 中同步处理每个数据报。
	//
	// 该模式省去队列和逐包缓冲池开销，适合执行时间很短的 action；慢 action 会阻塞后续数据报读取。
	UDPDispatchInline
)

// Option 描述一个 Server 构造选项。
//
// 选项只会在 New、NewTCP 或 NewUDP 构造期间应用；构造完成后配置保持不变。
type Option interface {
	apply(*serverConfig) error
}

// optionFunc 将普通函数适配为 Option。
type optionFunc func(*serverConfig) error

// apply 把当前选项应用到待校验的服务端配置。
func (fn optionFunc) apply(config *serverConfig) error {
	return fn(config)
}

// serverConfig 保存服务端的内部配置；New 返回后不再修改。
type serverConfig struct {
	network              Network
	address              string
	idleTimeout          time.Duration
	writeTimeout         time.Duration
	shutdownWriteTimeout time.Duration
	maxFrameSize         int
	maxConnections       int
	tlsConfig            *tls.Config
	tlsHandshakeTimeout  time.Duration
	logger               *slog.Logger
	udpWorkers           int
	udpQueueSize         int
	maxDatagramSize      int
	udpDispatchMode      UDPDispatchMode
}

// newServerConfig 创建默认配置，依次应用 options，并在最后执行跨字段校验。
//
// 参数中的 nil 选项会返回 ErrInvalidConfig；返回成功时配置可由 Server 并发只读访问。
func newServerConfig(network Network, address string, options ...Option) (serverConfig, error) {
	config := serverConfig{
		network:              network,
		address:              address,
		idleTimeout:          300 * time.Second,
		writeTimeout:         30 * time.Second,
		shutdownWriteTimeout: time.Second,
		maxFrameSize:         bufio.MaxScanTokenSize,
		tlsHandshakeTimeout:  10 * time.Second,
		logger:               slog.Default(),
		udpWorkers:           runtime.GOMAXPROCS(0),
		udpQueueSize:         64,
		maxDatagramSize:      64 * 1024,
		udpDispatchMode:      UDPDispatchQueued,
	}
	for _, option := range options {
		if option == nil {
			return serverConfig{}, fmt.Errorf("%w: nil option", ErrInvalidConfig)
		}
		if err := option.apply(&config); err != nil {
			return serverConfig{}, err
		}
	}
	if err := config.validate(); err != nil {
		return serverConfig{}, err
	}
	return config, nil
}

// validate 校验所有选项应用后的最终配置，包括传输层限制及字段间约束。
func (config serverConfig) validate() error {
	if config.network != TCP && config.network != UDP {
		return fmt.Errorf("%w: %q", ErrUnsupportedNetwork, config.network)
	}
	if config.address == "" {
		return fmt.Errorf("%w: address must not be empty", ErrInvalidConfig)
	}
	if config.idleTimeout < 0 || config.writeTimeout < 0 || config.shutdownWriteTimeout < 0 || config.tlsHandshakeTimeout < 0 {
		return fmt.Errorf("%w: timeouts must not be negative", ErrInvalidConfig)
	}
	if config.maxFrameSize <= 0 {
		return fmt.Errorf("%w: max frame size must be positive", ErrInvalidConfig)
	}
	if config.maxConnections < 0 {
		return fmt.Errorf("%w: max connections must not be negative", ErrInvalidConfig)
	}
	if config.tlsConfig != nil && config.network != TCP {
		return fmt.Errorf("%w: TLS is only supported for TCP", ErrInvalidConfig)
	}
	if config.logger == nil {
		return fmt.Errorf("%w: logger must not be nil", ErrInvalidConfig)
	}
	if config.udpWorkers <= 0 || config.udpQueueSize <= 0 {
		return fmt.Errorf("%w: UDP workers and queue size must be positive", ErrInvalidConfig)
	}
	if config.maxDatagramSize <= 0 || config.maxDatagramSize > 64*1024 {
		return fmt.Errorf("%w: max datagram size must be between 1 and 65536", ErrInvalidConfig)
	}
	if config.udpDispatchMode != UDPDispatchQueued && config.udpDispatchMode != UDPDispatchInline {
		return fmt.Errorf("%w: unsupported UDP dispatch mode %d", ErrInvalidConfig, config.udpDispatchMode)
	}
	return nil
}

// WithIdleTimeout 设置最大入站空闲时间，0 表示禁用空闲超时。
//
// TCP 通过刷新读截止时间实现；UDP 由服务级定时扫描器关闭长期无入站数据的远端会话。
func WithIdleTimeout(timeout time.Duration) Option {
	return optionFunc(func(config *serverConfig) error {
		config.idleTimeout = timeout
		return nil
	})
}

// WithWriteTimeout 设置 TCP 写入的默认超时时间，0 表示不设置默认写截止时间。
// 调用方 context 中更早的截止时间仍具有优先级。
func WithWriteTimeout(timeout time.Duration) Option {
	return optionFunc(func(config *serverConfig) error {
		config.writeTimeout = timeout
		return nil
	})
}

// WithShutdownWriteTimeout 设置优雅停机期间发送 EOF 标记的总等待时间。
// 该写入是尽力而为，超时后服务端会强制关闭剩余会话。
func WithShutdownWriteTimeout(timeout time.Duration) Option {
	return optionFunc(func(config *serverConfig) error {
		config.shutdownWriteTimeout = timeout
		return nil
	})
}

// WithMaxFrameSize 设置 TCP 拆包器允许接收的最大帧大小，单位为字节。
func WithMaxFrameSize(size int) Option {
	return optionFunc(func(config *serverConfig) error {
		config.maxFrameSize = size
		return nil
	})
}

// WithMaxConnections 设置同时处理的 TCP 连接数上限，0 表示不限制。
// 超出上限的新连接会在 accept 后立即关闭，不会创建 AppSession。
func WithMaxConnections(limit int) Option {
	return optionFunc(func(config *serverConfig) error {
		config.maxConnections = limit
		return nil
	})
}

// WithTLS 为 TCP 服务启用 TLS。
//
// tlsConfig 不能为 nil；函数会克隆配置，调用方之后修改原对象不会影响已构造的 Server。
func WithTLS(tlsConfig *tls.Config) Option {
	return optionFunc(func(config *serverConfig) error {
		if tlsConfig == nil {
			return fmt.Errorf("%w: TLS config must not be nil", ErrInvalidConfig)
		}
		config.tlsConfig = tlsConfig.Clone()
		return nil
	})
}

// WithTLSHandshakeTimeout 设置 TCP 会话准入前的 TLS 握手超时，0 表示不额外设置超时。
// 服务端开始关闭时，即使未到该超时，握手也会随服务器 context 一同取消。
func WithTLSHandshakeTimeout(timeout time.Duration) Option {
	return optionFunc(func(config *serverConfig) error {
		config.tlsHandshakeTimeout = timeout
		return nil
	})
}

// WithLogger 设置服务端记录运行错误和调试信息时使用的结构化日志器。
// logger 不能为 nil；其 Handler 应支持并发并避免永久阻塞，否则会延迟完整排空。
func WithLogger(logger *slog.Logger) Option {
	return optionFunc(func(config *serverConfig) error {
		config.logger = logger
		return nil
	})
}

// WithUDPWorkers 设置 queued 模式下有序 UDP 处理分片的数量。
// 同一远端地址会稳定映射到同一分片；该选项不影响 inline 模式。
func WithUDPWorkers(workers int) Option {
	return optionFunc(func(config *serverConfig) error {
		config.udpWorkers = workers
		return nil
	})
}

// WithUDPQueueSize 设置 queued 模式下每个 UDP worker 队列的容量。
// 队列满时新数据报会被丢弃并报告 ErrUDPQueueFull；该选项不影响 inline 模式。
func WithUDPQueueSize(size int) Option {
	return optionFunc(func(config *serverConfig) error {
		config.udpQueueSize = size
		return nil
	})
}

// WithMaxDatagramSize 设置服务端接受的最大 UDP 数据报大小，单位为字节。
// 合法范围为 1 到 65536；超限数据报会被丢弃并报告 ErrDatagramTooLarge。
func WithMaxDatagramSize(size int) Option {
	return optionFunc(func(config *serverConfig) error {
		config.maxDatagramSize = size
		return nil
	})
}

// WithUDPDispatchMode 选择 UDP 的有界队列调度或同步低时延处理模式。
func WithUDPDispatchMode(mode UDPDispatchMode) Option {
	return optionFunc(func(config *serverConfig) error {
		config.udpDispatchMode = mode
		return nil
	})
}
