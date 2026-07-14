package goserver

import "sync/atomic"

// Stats 表示 Server 单次生命周期内的瞬时累计统计快照。
//
// 所有计数器从构造时开始累计，不按时间窗口自动归零。字段由独立原子操作读取，
// 因此适合运行监控，但跨字段关系不构成事务一致性快照。
type Stats struct {
	// AcceptedConnections 是通过握手、连接过滤和服务状态校验后成功准入的 TCP 或 UDP 会话数。
	// 因连接上限、握手失败、过滤拒绝或停止竞态而未建立会话的连接不计入。
	AcceptedConnections uint64
	// ClosedSessions 是完成最终关闭流程的会话数。
	ClosedSessions uint64
	// BytesRead 对 TCP 累计拆包后的 token 字节数，对 UDP 累计从 socket 读出的完整数据报字节数。
	// 因此 TCP 分隔符通常不包含在内，而 UDP 超限后被丢弃的数据报仍包含在内。
	BytesRead uint64
	// BytesWritten 是底层传输实际成功写出的字节数。
	BytesWritten uint64
	// PacketsRead 是从 UDP socket 成功读出的数据报数；TCP 下保持为 0。
	PacketsRead uint64
	// PacketsDropped 是因服务端数据报大小限制或 queued 队列满而主动丢弃的 UDP 包数。
	// 内核 socket 缓冲区溢出等服务端不可见的丢包不在此统计中。
	PacketsDropped uint64
	// ProtocolErrors 是被实现明确归类为协议解析失败的累计次数。
	// UDP 包含拆包和 action 路径解析错误；TCP 当前累计 action 路径解析错误。
	ProtocolErrors uint64
}

// serverStats 使用独立原子计数器维护服务端热路径统计。
type serverStats struct {
	acceptedConnections atomic.Uint64
	closedSessions      atomic.Uint64
	bytesRead           atomic.Uint64
	bytesWritten        atomic.Uint64
	packetsRead         atomic.Uint64
	packetsDropped      atomic.Uint64
	protocolErrors      atomic.Uint64
}

// snapshot 逐字段无锁读取当前计数，生成适合监控用途的 Stats 快照。
func (stats *serverStats) snapshot() Stats {
	return Stats{
		AcceptedConnections: stats.acceptedConnections.Load(),
		ClosedSessions:      stats.closedSessions.Load(),
		BytesRead:           stats.bytesRead.Load(),
		BytesWritten:        stats.bytesWritten.Load(),
		PacketsRead:         stats.packetsRead.Load(),
		PacketsDropped:      stats.packetsDropped.Load(),
		ProtocolErrors:      stats.protocolErrors.Load(),
	}
}
