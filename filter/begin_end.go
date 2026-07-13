package filter

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
)

var (
	// ErrInvalidMarkLength 表示开始或结束标记为空，无法安全拆包。
	ErrInvalidMarkLength = errors.New("begin and end marks must not be empty")

	// ErrInvalidPackageLength 表示包长度不满足协议要求。
	ErrInvalidPackageLength = errors.New("invalid package length")

	// ErrInvalidTokenLength 表示 token 长度不足以解析协议头。
	ErrInvalidTokenLength = errors.New("invalid token length")

	// ErrInvalidActionNameLength 表示 action 名称长度超过 token 剩余长度。
	ErrInvalidActionNameLength = errors.New("invalid action name length")
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
		if beginLength == 0 || endLength == 0 {
			return 0, nil, ErrInvalidMarkLength
		}
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		start, end := 0, 0
		if start = bytes.Index(data, s.Begin); start < 0 {
			if atEOF {
				return 0, nil, ErrInvalidPackageLength
			}
			return 0, nil, nil
		}
		endOffset := bytes.Index(data[start+beginLength:], s.End)
		if endOffset < 0 {
			if atEOF {
				return 0, nil, ErrInvalidPackageLength
			}
			return 0, nil, nil
		}
		end = start + beginLength + endOffset
		return end + endLength, data[start+beginLength : end], nil
	}
}

// ResolveAction 返回解析函数
func (s *BeginEndMarkReceiveFilter) ResolveAction() ResolveActionFunc {
	return func(token []byte) (actionName string, msg []byte, err error) {
		if len(token) < 4 {
			err = ErrInvalidTokenLength
			return
		}
		actionNameLength := uint32(0)
		err = binary.Read(bytes.NewReader(token[0:4]), binary.BigEndian, &actionNameLength)
		if err != nil {
			return
		}
		if actionNameLength > uint32(len(token)-4) {
			err = ErrInvalidActionNameLength
			return
		}
		actionEnd := 4 + int(actionNameLength)
		actionName = string(token[4:actionEnd])
		msg = token[actionEnd:]
		return
	}
}
