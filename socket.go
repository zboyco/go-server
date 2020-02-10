package go_server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
)

type receiveSplit interface {
	SplitFunc() bufio.SplitFunc
	ResolveAction(token []byte) (actionName string, msg []byte)
}

func (server *Server) SetReceiveSplit(s receiveSplit) {

}

type BeginEndMarkReceiveSplit struct {
	Begin string
	End   string
}

func (s *BeginEndMarkReceiveSplit) SplitFunc() bufio.SplitFunc {
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF {
			return 0, nil, errors.New("EOF")
		}
		if data[0] != '$' || data[3] != '#' {
			return 0, nil, errors.New("数据异常")
		}
		if len(data) > 4 {
			length := int16(0)
			binary.Read(bytes.NewReader(data[1:3]), binary.BigEndian, &length)
			if int(length)+4 <= len(data) {
				return int(length) + 4, data[4 : int(length)+4], nil
			}
		}
		return 0, nil, nil
	}
}

func (s *BeginEndMarkReceiveSplit) ResolveAction(token []byte) (actionName string, msg []byte) {
	return "", nil
}

// FixedHeaderReceiveSplit 固定头部协议
// 头部占用8个字节
// 1-4位代表数据Body长度
// 5-8位代表ActionName长度
// 剩余为数据Body
type FixedHeaderReceiveSplit struct {
	PackageLength    int
	ActionNameLength int
}

func (s *FixedHeaderReceiveSplit) SplitFunc() bufio.SplitFunc {
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF {
			return 0, nil, errors.New("EOF")
		}
		if len(data) > 4 {
			packageLength := 0
			err := binary.Read(bytes.NewReader(data[0:4]), binary.BigEndian, &packageLength)
			if err != nil {
				return 0, nil, err
			}
			if packageLength <= len(data) {
				return packageLength, data[:packageLength], nil
			}
		}
		return 0, nil, nil
	}
}

func (s *FixedHeaderReceiveSplit) ResolveAction(token []byte) (actionName string, msg []byte, err error) {
	actionNameLength := 0
	err = binary.Read(bytes.NewReader(token[4:8]), binary.BigEndian, &actionNameLength)
	if err != nil {
		return
	}
	actionName = string(token[8 : 8+actionNameLength])
	msg = token[8+actionNameLength:]
	return
}
