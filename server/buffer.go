package server

import (
	"errors"
	"io"
)

type buffer struct {
	reader io.Reader //
	buf    []byte    //缓存
	start  int       //有效位开始索引
	end    int       //有效位结束索引
}

func newBuffer(reader io.Reader, len int) *buffer {
	buf := make([]byte, len)
	return &buffer{reader, buf, 0, 0}
}

func (instance *buffer) len() int {
	return instance.end - instance.start
}

// 清理缓存中已提取的数据
func (instance *buffer) cleanBuf() {
	if instance.start == 0 {
		return
	}
	copy(instance.buf, instance.buf[instance.start:instance.end])
	instance.end -= instance.start
	instance.start = 0
}

// 接收数据
func (instance *buffer) read() (int, error) {
	instance.cleanBuf()
	n, err := instance.reader.Read(instance.buf[instance.end:])
	if err != nil {
		return n, err
	}
	instance.end += n
	return n, nil
}

// 查看指定长度字节数据
func (instance *buffer) peek(len int) ([]byte, error) {
	if instance.len() < len {
		return nil, errors.New("可读取长度不够")
	}
	result := instance.buf[instance.start : instance.start+len]
	return result, nil
}

// 提取指定长度字节数据
func (instance *buffer) pick(offset int, len int) ([]byte, error) {
	result, err := instance.peek(offset + len)
	if err != nil {
		return nil, err
	}
	instance.start += (offset + len)
	return result[offset:], nil
}
