package filter

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
)

// BeginEndMarkReceiveFilter 标记数据包开始和结尾字节
// 数据包以Begin开始，End结尾
// 中间1-4位代表ActionName长度
// 剩余部分为 ActionName字符串 + 数据Body
type BeginEndMarkReceiveFilter struct {
	Begin []byte
	End   []byte
}

// SplitFunc 返回拆包函数
func (s *BeginEndMarkReceiveFilter) SplitFunc() bufio.SplitFunc {
	// 预先计算标识位的长度
	beginLen := len(s.Begin)
	endLen := len(s.End)
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		// --- 步骤 1: 查找起始标识位 ---
		start := bytes.Index(data, s.Begin)
		if start == -1 {
			if atEOF && len(data) > 0 {
				// 如果流结束了，但缓冲区里还有数据，并且我们没找到任何起始符，
				// 那么这些数据都是无效的，全部丢弃。
				return len(data), nil, nil
			}
			// 在当前缓冲中未找到起始标识，需要更多数据。
			// 返回 0, nil, nil 是告诉 bufio.Scanner "数据还不够，继续读"。
			return 0, nil, nil
		}
		// --- 步骤 2: 丢弃起始标识位前的所有无效数据---
		if start > 0 {
			// 如果起始标识位不在缓冲区的开头，说明前面有垃圾数据。
			// 返回 start, nil, nil 是告诉 bufio.Scanner "丢弃前面的 start 字节，然后重试"。
			return start, nil, nil
		}
		// --- 步骤 3: 在有效数据中查找结束标识位 ---
		// 此时，数据保证是以 s.Begin 开头。我们从 s.Begin 之后开始搜索 s.End。
		// 这样可以防止 s.End 的内容与 s.Begin 的部分内容碰巧相同而导致误判。
		searchStart := beginLen
		end := bytes.Index(data[searchStart:], s.End)
		if end == -1 {
			if atEOF {
				// 如果流已经结束，但我们还没找到结束标识，
				// 这意味着这是一个不完整/损坏的帧。返回一个错误是合理的。
				return 0, nil, errors.New("unclosed frame at EOF")
			}
			// 结束标识位还未到达，需要更多数据。
			return 0, nil, nil
		}
		// 修正 end 的索引，使其相对于 data 的起始位置
		end += searchStart
		// --- 步骤 4: 成功找到一个完整的帧 ---
		// 计算这个完整帧的总长度（包括开始和结束标识）
		advance = end + endLen

		// 返回包含标识位的完整帧
		// 这是零拷贝操作，非常高效，但调用方必须立即处理或拷贝返回的 token，
		// 因为底层的数组可能在下一次 Scan() 调用时被覆盖。
		token = data[:advance]

		return advance, token, nil
	}
}

// ResolveAction 返回解析函数
func (s *BeginEndMarkReceiveFilter) ResolveAction() ResolveActionFunc {
	beginLen := len(s.Begin)
	endLen := len(s.End)
	return func(token []byte) (actionName string, msg []byte, err error) {
		// 跳过 Begin 标识位，去掉 End 标识位
		data := token[beginLen : len(token)-endLen]

		actionNameLength := uint32(0)
		err = binary.Read(bytes.NewReader(data[0:4]), binary.BigEndian, &actionNameLength)
		if err != nil {
			return
		}
		actionName = string(data[4 : 4+actionNameLength])
		msg = data[4+actionNameLength:]
		return
	}
}
