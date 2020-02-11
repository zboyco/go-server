package go_server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
)

type ResolveActionFunc func(token []byte) (actionName string, msg []byte, err error)
type ReceiveFilter interface {
	SplitFunc() bufio.SplitFunc
	ResolveAction() ResolveActionFunc
}

func (server *Server) SetReceiveFilter(s ReceiveFilter) {
	server.splitFunc = s.SplitFunc()
	server.resolveAction = s.ResolveAction()
}

// BeginEndMarkReceiveFilter 标记数据包开始和结尾字节
// 数据包以Begin开始，End结尾
// 中间部分为 ActionName字符串 + 数据Body
type BeginEndMarkReceiveFilter struct {
	Begin []byte
	End   []byte
}

func (s *BeginEndMarkReceiveFilter) SplitFunc() bufio.SplitFunc {
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF {
			return 0, nil, errors.New("EOF")
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
		beginLength := len(s.Begin)
		packageLength := end - start - beginLength
		return packageLength + beginLength + len(s.End), data[beginLength : beginLength+packageLength], nil
	}
}
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

// FixedHeaderReceiveFilter 固定头部协议
// 头部占用8个字节
// 1-4位代表数据包总长度
// 5-8位代表ActionName长度
// 剩余为 ActionName字符串 + 数据Body
type FixedHeaderReceiveFilter struct {
}

func (s *FixedHeaderReceiveFilter) SplitFunc() bufio.SplitFunc {
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF {
			return 0, nil, errors.New("EOF")
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
