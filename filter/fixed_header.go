package filter

import (
	"bufio"
	"encoding/binary"
)

// FixedHeaderReceiveFilter 解析带 8 字节固定头的协议帧。
// 前 4 字节是包含头部在内的总长度，后 4 字节是 action 路径长度，二者均使用大端序。
type FixedHeaderReceiveFilter struct{}

// SplitFunc 返回按头部总长度字段拆包的 bufio.SplitFunc。
// 总长度小于 8 或流结束时数据不足会返回协议错误；token 包含完整的 8 字节头部。
func (*FixedHeaderReceiveFilter) SplitFunc() bufio.SplitFunc {
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if len(data) < 4 {
			if atEOF {
				return len(data), nil, ErrInvalidTokenLength
			}
			return 0, nil, nil
		}
		packageLength := binary.BigEndian.Uint32(data[:4])
		if packageLength < 8 {
			return 0, nil, ErrInvalidPackageLength
		}
		// 先与当前切片长度比较，保证后续 uint32 到 int 的转换在 32 位平台也不会溢出。
		if uint64(packageLength) <= uint64(len(data)) {
			length := int(packageLength)
			return length, data[:length], nil
		}
		if atEOF {
			return len(data), nil, ErrInvalidPackageLength
		}
		return 0, nil, nil
	}
}

// ResolveAction 从完整固定头帧中零拷贝解析 action 路径和 payload。
// 声明的 action 长度超过帧剩余内容时返回 ErrInvalidActionNameLength。
func (*FixedHeaderReceiveFilter) ResolveAction() ResolveActionFunc {
	return func(token []byte) (string, []byte, error) {
		if len(token) < 8 {
			return "", nil, ErrInvalidTokenLength
		}
		actionLength := binary.BigEndian.Uint32(token[4:8])
		if actionLength > uint32(len(token)-8) {
			return "", nil, ErrInvalidActionNameLength
		}
		actionEnd := 8 + int(actionLength)
		return string(token[8:actionEnd]), token[actionEnd:], nil
	}
}
