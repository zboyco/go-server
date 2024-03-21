package filter

import (
	"bufio"
	"bytes"
	"encoding/binary"
)

// FixedHeaderReceiveFilter 固定头部协议
// 头部占用8个字节
// 1-4位代表数据包总长度
// 5-8位代表ActionName长度
// 剩余为 ActionName字符串 + 数据Body
type FixedHeaderReceiveFilter struct{}

// SplitFunc 返回拆包函数
func (s *FixedHeaderReceiveFilter) SplitFunc() bufio.SplitFunc {
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF {
			return 0, nil, nil
		}
		if len(data) > 4 {
			packageLength := uint32(0)
			err := binary.Read(bytes.NewReader(data[0:4]), binary.BigEndian, &packageLength)
			if err != nil {
				return 0, nil, err
			}
			if int(packageLength) <= len(data) {
				return int(packageLength), data[:packageLength], nil
			}
		}
		return 0, nil, nil
	}
}

// ResolveAction 返回解析函数
func (s *FixedHeaderReceiveFilter) ResolveAction() ResolveActionFunc {
	return func(token []byte) (actionName string, msg []byte, err error) {
		actionNameLength := uint32(0)
		err = binary.Read(bytes.NewReader(token[4:8]), binary.BigEndian, &actionNameLength)
		if err != nil {
			return
		}
		actionName = string(token[8 : 8+actionNameLength])
		msg = token[8+actionNameLength:]
		return
	}
}
