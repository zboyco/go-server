package filter

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// TestBeginEndResolveActionRejectsInvalidToken 验证开始结束标记协议不会因畸形 token panic。
func TestBeginEndResolveActionRejectsInvalidToken(t *testing.T) {
	resolve := (&BeginEndMarkReceiveFilter{}).ResolveAction()

	if _, _, err := resolve([]byte{1, 2, 3}); !errors.Is(err, ErrInvalidTokenLength) {
		t.Fatalf("short token error = %v, want %v", err, ErrInvalidTokenLength)
	}

	head := make([]byte, 4)
	binary.BigEndian.PutUint32(head, 8)
	if _, _, err := resolve(head); !errors.Is(err, ErrInvalidActionNameLength) {
		t.Fatalf("invalid action length error = %v, want %v", err, ErrInvalidActionNameLength)
	}
}

// TestBeginEndSplitWithPrefixGarbage 验证 begin 前存在垃圾数据时仍能返回正确 token。
func TestBeginEndSplitWithPrefixGarbage(t *testing.T) {
	filter := &BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	}
	token := beginEndToken("/say", []byte("hello"))
	packet := bytes.Join([][]byte{[]byte("xx"), filter.Begin, token, filter.End}, nil)

	advance, got, err := filter.SplitFunc()(packet, false)
	if err != nil {
		t.Fatalf("SplitFunc returned error: %v", err)
	}
	if advance != len(packet) {
		t.Fatalf("advance = %d, want %d", advance, len(packet))
	}
	if !bytes.Equal(got, token) {
		t.Fatalf("token = %q, want %q", got, token)
	}
}

// TestBeginEndSplitCompletePacketAtEOF 验证 EOF 到来时已有完整包不会被丢弃。
func TestBeginEndSplitCompletePacketAtEOF(t *testing.T) {
	filter := &BeginEndMarkReceiveFilter{
		Begin: []byte{'!', '$'},
		End:   []byte{'$', '!'},
	}
	token := beginEndToken("/say", []byte("hello"))
	packet := bytes.Join([][]byte{filter.Begin, token, filter.End}, nil)

	advance, got, err := filter.SplitFunc()(packet, true)
	if err != nil {
		t.Fatalf("SplitFunc returned error: %v", err)
	}
	if advance != len(packet) {
		t.Fatalf("advance = %d, want %d", advance, len(packet))
	}
	if !bytes.Equal(got, token) {
		t.Fatalf("token = %q, want %q", got, token)
	}
}

// TestFixedHeaderSplitRejectsInvalidPackageLength 验证固定头协议拒绝短于协议头的包长。
func TestFixedHeaderSplitRejectsInvalidPackageLength(t *testing.T) {
	filter := &FixedHeaderReceiveFilter{}
	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data, 4)

	if _, _, err := filter.SplitFunc()(data, false); !errors.Is(err, ErrInvalidPackageLength) {
		t.Fatalf("invalid package length error = %v, want %v", err, ErrInvalidPackageLength)
	}
}

// TestFixedHeaderResolveActionRejectsInvalidToken 验证固定头协议不会因畸形 token panic。
func TestFixedHeaderResolveActionRejectsInvalidToken(t *testing.T) {
	resolve := (&FixedHeaderReceiveFilter{}).ResolveAction()

	if _, _, err := resolve([]byte{1, 2, 3}); !errors.Is(err, ErrInvalidTokenLength) {
		t.Fatalf("short token error = %v, want %v", err, ErrInvalidTokenLength)
	}

	token := make([]byte, 8)
	binary.BigEndian.PutUint32(token[0:4], 8)
	binary.BigEndian.PutUint32(token[4:8], 16)
	if _, _, err := resolve(token); !errors.Is(err, ErrInvalidActionNameLength) {
		t.Fatalf("invalid action length error = %v, want %v", err, ErrInvalidActionNameLength)
	}
}

func beginEndToken(action string, body []byte) []byte {
	head := make([]byte, 4)
	binary.BigEndian.PutUint32(head, uint32(len(action)))
	return bytes.Join([][]byte{head, []byte(action), body}, nil)
}
