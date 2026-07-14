package filter

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
)

var (
	// ErrInvalidMarkLength 表示 Begin 或 End 标记为空，无法形成可判定边界的协议帧。
	ErrInvalidMarkLength = errors.New("begin and end marks must not be empty")
	// ErrInvalidPackageLength 表示输入在流结束或数据报结束时仍不能组成完整协议包。
	ErrInvalidPackageLength = errors.New("invalid package length")
	// ErrInvalidTokenLength 表示完整帧长度不足以包含协议规定的固定字段。
	ErrInvalidTokenLength = errors.New("invalid token length")
	// ErrInvalidActionNameLength 表示声明的 action 长度超过当前帧剩余字节数。
	ErrInvalidActionNameLength = errors.New("invalid action name length")
)

// BeginEndMarkReceiveFilter 使用固定字节序列标识每个应用层包的开始和结束。
// 标记本身不会出现在 SplitFunc 返回的 token 中。
type BeginEndMarkReceiveFilter struct {
	// Begin 是非空的包起始标记。
	Begin []byte
	// End 是非空的包结束标记。
	End []byte
}

// SplitFunc 返回基于 Begin 和 End 拆包的 bufio.SplitFunc。
//
// 函数会丢弃已确认不可能成为 Begin 前缀的垃圾字节，但保留最多 len(Begin)-1 个尾部字节，
// 以处理跨 TCP Read 的起始标记；atEOF 时仍不完整的包返回 ErrInvalidPackageLength。
func (filter *BeginEndMarkReceiveFilter) SplitFunc() bufio.SplitFunc {
	beginLength := len(filter.Begin)
	endLength := len(filter.End)
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if beginLength == 0 || endLength == 0 {
			return 0, nil, ErrInvalidMarkLength
		}
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		start := bytes.Index(data, filter.Begin)
		if start < 0 {
			if atEOF {
				return len(data), nil, ErrInvalidPackageLength
			}
			keep := beginLength - 1
			if len(data) > keep {
				return len(data) - keep, nil, nil
			}
			return 0, nil, nil
		}
		endOffset := bytes.Index(data[start+beginLength:], filter.End)
		if endOffset < 0 {
			if atEOF {
				return len(data), nil, ErrInvalidPackageLength
			}
			if start > 0 {
				return start, nil, nil
			}
			return 0, nil, nil
		}
		end := start + beginLength + endOffset
		return end + endLength, data[start+beginLength : end], nil
	}
}

// ResolveAction 返回零拷贝的 action 与 payload 解析函数。
// token 格式为 4 字节大端 action 长度、action 字节及剩余 payload；返回 payload 引用 token 内存。
func (filter *BeginEndMarkReceiveFilter) ResolveAction() ResolveActionFunc {
	return func(token []byte) (string, []byte, error) {
		if len(token) < 4 {
			return "", nil, ErrInvalidTokenLength
		}
		actionLength := binary.BigEndian.Uint32(token[:4])
		if actionLength > uint32(len(token)-4) {
			return "", nil, ErrInvalidActionNameLength
		}
		actionEnd := 4 + int(actionLength)
		return string(token[4:actionEnd]), token[actionEnd:], nil
	}
}
