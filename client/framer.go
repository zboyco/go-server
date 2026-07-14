package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"

	goserver "github.com/zboyco/go-server/v2"
)

// frameReader 在有界缓冲区上执行 SplitFunc，并在暂时性 TCP 读取错误之间保留未完成数据。
type frameReader struct {
	split       bufio.SplitFunc
	buffer      []byte
	initialSize int
	maxSize     int
	pendingErr  error
	finished    bool
}

// newFrameReader 创建初始容量和最大容量均已校验的、可在暂时错误后重试的流式拆包器。
func newFrameReader(split bufio.SplitFunc, initialSize, maxSize int) *frameReader {
	return &frameReader{split: split, initialSize: initialSize, maxSize: maxSize}
}

// Read 返回一个完整帧，并在超时等暂时网络错误后保留已经读取的部分字节。
//
// SplitFunc 的 token 会复制后返回，避免后续缓冲区移动覆盖结果。非法 advance、超过最大帧、
// 拆包协议错误或 EOF 会重置终止状态；bufio.ErrFinalToken 携带的最后一个 token 会正常返回一次。
func (reader *frameReader) Read(conn net.Conn) ([]byte, error) {
	if reader.finished {
		return nil, io.EOF
	}
	for {
		atEOF := errors.Is(reader.pendingErr, io.EOF)
		if len(reader.buffer) > 0 || atEOF {
			advance, token, err := reader.split(reader.buffer, atEOF)
			finalToken := errors.Is(err, bufio.ErrFinalToken)
			if err != nil && !finalToken {
				reader.reset()
				return nil, err
			}
			if advance < 0 || advance > len(reader.buffer) || (token != nil && advance == 0 && !finalToken) {
				reader.reset()
				return nil, fmt.Errorf("%w: split advance %d for %d buffered bytes", goserver.ErrInvalidFrame, advance, len(reader.buffer))
			}
			var result []byte
			if token != nil {
				result = append([]byte(nil), token...)
			}
			if advance > 0 {
				remaining := copy(reader.buffer, reader.buffer[advance:])
				reader.buffer = reader.buffer[:remaining]
			}
			if finalToken {
				reader.reset()
				reader.finished = true
				if token != nil {
					return result, nil
				}
				return nil, io.EOF
			}
			if token != nil {
				return result, nil
			}
			if advance > 0 {
				continue
			}
			if atEOF {
				reader.reset()
				return nil, io.EOF
			}
		}

		if reader.pendingErr != nil {
			err := reader.pendingErr
			reader.pendingErr = nil
			return nil, err
		}
		if len(reader.buffer) >= reader.maxSize {
			reader.reset()
			return nil, fmt.Errorf("%w: framed response exceeds %d bytes", goserver.ErrInvalidFrame, reader.maxSize)
		}
		reader.grow()
		start := len(reader.buffer)
		readBuffer := reader.buffer[:cap(reader.buffer)]
		size, readErr := conn.Read(readBuffer[start:])
		reader.buffer = readBuffer[:start+size]
		if readErr != nil {
			reader.pendingErr = readErr
		}
		if size == 0 && readErr == nil {
			return nil, io.ErrNoProgress
		}
	}
}

// grow 以倍增方式扩充读取缓冲区，但容量始终不超过 maxSize。
func (reader *frameReader) grow() {
	if len(reader.buffer) < cap(reader.buffer) {
		return
	}
	nextSize := reader.initialSize
	if current := cap(reader.buffer); current > 0 {
		nextSize = current * 2
	}
	if nextSize > reader.maxSize {
		nextSize = reader.maxSize
	}
	expanded := make([]byte, len(reader.buffer), nextSize)
	copy(expanded, reader.buffer)
	reader.buffer = expanded
}

// reset 丢弃 EOF 或协议错误后的缓冲内容及待返回错误；finished 标志由调用方按语义管理。
func (reader *frameReader) reset() {
	reader.buffer = reader.buffer[:0]
	reader.pendingErr = nil
}

// splitUDPDatagram 从一个 UDP 数据报提取全部完整帧，绝不把未消费字节带到下一数据报。
//
// 每个 token 都会复制到调用方拥有的切片。零推进、非法 advance、无完整帧及拆包错误返回
// goserver.ErrInvalidFrame 或原始错误；bufio.ErrFinalToken 携带的 token 会作为最后一帧保留。
func splitUDPDatagram(data []byte, split bufio.SplitFunc) ([][]byte, error) {
	frames := make([][]byte, 0, 1)
	for len(data) > 0 {
		advance, token, err := split(data, true)
		finalToken := errors.Is(err, bufio.ErrFinalToken)
		if err != nil && !finalToken {
			return nil, err
		}
		if advance < 0 || advance > len(data) || (advance == 0 && !finalToken) {
			return nil, fmt.Errorf("%w: split advance %d for %d datagram bytes", goserver.ErrInvalidFrame, advance, len(data))
		}
		if token != nil {
			frames = append(frames, append([]byte(nil), token...))
		}
		if finalToken {
			break
		}
		data = data[advance:]
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("%w: UDP datagram contains no complete frame", goserver.ErrInvalidFrame)
	}
	return frames, nil
}
