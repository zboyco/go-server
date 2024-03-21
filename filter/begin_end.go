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
	beginLength := len(s.Begin)
	endLength := len(s.End)
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF {
			return 0, nil, nil
		}
		start, end := 0, 0
		if start = bytes.Index(data, s.Begin); start < 0 {
			// We have a full newline-terminated line.
			return 0, nil, nil
		}
		if end = bytes.Index(data, s.End); end < 0 {
			// We have a full newline-terminated line.
			return 0, nil, nil
		}
		if start > end {
			return 0, nil, errors.New("数据异常")
		}
		packageLength := end - start - beginLength
		return packageLength + beginLength + endLength, data[beginLength : beginLength+packageLength], nil
	}
}

// ResolveAction 返回解析函数
func (s *BeginEndMarkReceiveFilter) ResolveAction() ResolveActionFunc {
	return func(token []byte) (actionName string, msg []byte, err error) {
		actionNameLength := uint32(0)
		err = binary.Read(bytes.NewReader(token[0:4]), binary.BigEndian, &actionNameLength)
		if err != nil {
			return
		}
		actionName = string(token[4 : 4+actionNameLength])
		msg = token[4+actionNameLength:]
		return
	}
}
