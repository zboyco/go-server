package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	goserver "github.com/zboyco/go-server"
	"github.com/zboyco/go-server/filter"
)

// TestNewSimpleClientRejectsInvalidConfig verifies constructor failures are explicit.
func TestNewSimpleClientRejectsInvalidConfig(t *testing.T) {
	if _, err := NewSimpleClient(goserver.Network("invalid"), "127.0.0.1:1"); !errors.Is(err, goserver.ErrUnsupportedNetwork) {
		t.Fatalf("invalid network error = %v", err)
	}
	if _, err := NewSimpleClient(goserver.TCP, ""); err == nil {
		t.Fatal("empty address should fail")
	}
	if _, err := NewSimpleClient(goserver.UDP, "127.0.0.1:1", WithTLS(&tlsTestConfig)); err == nil {
		t.Fatal("UDP TLS should fail")
	}
}

// TestClientOptionsAndDisconnectedErrors covers v1 options and closed-state behavior.
func TestClientOptionsAndDisconnectedErrors(t *testing.T) {
	client, err := NewSimpleClient(goserver.TCP, "127.0.0.1:1",
		WithBufferSize(32),
		WithMaxScanTokenSize(128),
		WithDialTimeout(time.Millisecond),
		WithReadTimeout(time.Second),
		WithWriteTimeout(time.Second),
		WithMaxDatagramSize(4096),
	)
	if err != nil {
		t.Fatal(err)
	}
	if client.config.bufferSize != 32 || client.config.maxScanTokenSize != 128 || client.config.maxDatagramSize != 4096 {
		t.Fatalf("client config = %+v", client.config)
	}
	if err := client.Send(context.Background(), []byte("x")); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("disconnected Send error = %v", err)
	} else if errors.Is(err, goserver.ErrSessionClosed) {
		t.Fatalf("disconnected Send still matched ErrSessionClosed: %v", err)
	}
	if _, err := client.Receive(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("disconnected Receive error = %v", err)
	} else if errors.Is(err, goserver.ErrSessionClosed) {
		t.Fatalf("disconnected Receive still matched ErrSessionClosed: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err == nil {
		t.Fatal("dialing a closed local port should fail")
	}

	invalidOptions := [][]Option{
		{nil},
		{WithBufferSize(0)},
		{WithBufferSize(128), WithMaxScanTokenSize(64)},
		{WithDialTimeout(-1)},
		{WithReadTimeout(-1)},
		{WithWriteTimeout(-1)},
		{WithScannerSplitFunc(nil)},
		{WithTLS(nil)},
		{WithMaxDatagramSize(0)},
		{WithMaxDatagramSize(64*1024 + 1)},
	}
	for _, options := range invalidOptions {
		if _, err := NewSimpleClient(goserver.TCP, "127.0.0.1:1", options...); err == nil {
			t.Fatalf("invalid options %#v should fail", options)
		}
	}
}

// TestSimpleClientRawRoundTrip verifies the raw Receive path.
func TestSimpleClientRawRoundTrip(t *testing.T) {
	_, address := startClientTestServer(t, func(_ context.Context, _ *goserver.AppSession, token []byte) ([]byte, error) {
		return append([]byte("raw:"), token...), nil
	})
	client, err := NewSimpleClient(goserver.TCP, address, WithBufferSize(64))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Send(context.Background(), []byte("value\n")); err != nil {
		t.Fatal(err)
	}
	response, err := client.Receive(context.Background())
	if err != nil || string(response) != "raw:value" {
		t.Fatalf("response = %q, %v", response, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Send(canceled, []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Send error = %v", err)
	}
}

// TestBeginEndMarkClientRejectsNilFilter verifies framed-client validation.
func TestBeginEndMarkClientRejectsNilFilter(t *testing.T) {
	if _, err := NewBeginEndMarkClient(goserver.TCP, "127.0.0.1:1", nil); err == nil {
		t.Fatal("nil filter should fail")
	}
}

var tlsTestConfig = tls.Config{InsecureSkipVerify: true} // #nosec G402 -- test-only handshake configuration.

// TestSimpleClientScannerReconnect verifies Scanner state is rebuilt for a new connection.
func TestSimpleClientScannerReconnect(t *testing.T) {
	server, address := startClientTestServer(t, func(_ context.Context, _ *goserver.AppSession, token []byte) ([]byte, error) {
		response := append([]byte(nil), token...)
		return append(response, '\n'), nil
	})
	_ = server
	client, err := NewSimpleClient(goserver.TCP, address, WithScannerSplitFunc(bufio.ScanLines))
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"first", "second"} {
		if err := client.Connect(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := client.Send(context.Background(), []byte(message+"\n")); err != nil {
			t.Fatal(err)
		}
		response, err := client.Receive(context.Background())
		if err != nil || string(response) != message {
			t.Fatalf("response = %q, %v", response, err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSimpleClientReceiveHonorsContext verifies cancellation interrupts a blocked read.
func TestSimpleClientReceiveHonorsContext(t *testing.T) {
	_, address := startClientTestServer(t, func(context.Context, *goserver.AppSession, []byte) ([]byte, error) {
		return nil, nil
	})
	client, err := NewSimpleClient(goserver.TCP, address, WithReadTimeout(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Receive(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Receive error = %v", err)
	}
}

// TestSimpleClientFramedReceiveRecoversAfterTimeout verifies partial TCP data is retained.
func TestSimpleClientFramedReceiveRecoversAfterTimeout(t *testing.T) {
	_, address := startClientTestServer(t, func(_ context.Context, session *goserver.AppSession, _ []byte) ([]byte, error) {
		if err := session.SendRaw(context.Background(), []byte("par")); err != nil {
			return nil, err
		}
		go func() {
			time.Sleep(80 * time.Millisecond)
			_ = session.SendRaw(context.Background(), []byte("tial\n"))
		}()
		return nil, nil
	})
	client, err := NewSimpleClient(goserver.TCP, address,
		WithScannerSplitFunc(bufio.ScanLines),
		WithReadTimeout(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Send(context.Background(), []byte("start\n")); err != nil {
		t.Fatal(err)
	}
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = client.Receive(firstCtx)
	firstCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Receive error = %v", err)
	}
	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	response, err := client.Receive(secondCtx)
	if err != nil || string(response) != "partial" {
		t.Fatalf("second response = %q, %v", response, err)
	}
}

// TestSimpleClientUDPFramingPreservesDatagramBoundaries verifies strict packet parsing.
func TestSimpleClientUDPFramingPreservesDatagramBoundaries(t *testing.T) {
	serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()
	receiveFilter := &filter.BeginEndMarkReceiveFilter{Begin: []byte("!$"), End: []byte("$!")}
	client, err := NewSimpleClient(goserver.UDP, serverConn.LocalAddr().String(), WithScannerSplitFunc(receiveFilter.SplitFunc()))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Send(context.Background(), []byte("register")); err != nil {
		t.Fatal(err)
	}
	if err := serverConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	_, remoteAddr, err := serverConn.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	for _, packet := range [][]byte{[]byte("!$hel"), []byte("lo$!")} {
		if _, err := serverConn.WriteToUDP(packet, remoteAddr); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, receiveErr := client.Receive(ctx)
		cancel()
		if !errors.Is(receiveErr, filter.ErrInvalidPackageLength) {
			t.Fatalf("partial datagram error = %v", receiveErr)
		}
	}
	if _, err := serverConn.WriteToUDP([]byte("!$one$!!$two$!"), remoteAddr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"one", "two"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		response, receiveErr := client.Receive(ctx)
		cancel()
		if receiveErr != nil || string(response) != want {
			t.Fatalf("UDP frame = %q, %v; want %q", response, receiveErr, want)
		}
	}
}

// TestSimpleClientUDPLargeDatagram verifies complete reads and configured rejection.
func TestSimpleClientUDPLargeDatagram(t *testing.T) {
	serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()
	if err := serverConn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		options []Option
		size    int
		wantErr error
	}{
		{name: "default", size: 6000},
		{name: "configured limit", options: []Option{WithMaxDatagramSize(1024)}, size: 2048, wantErr: goserver.ErrDatagramTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewSimpleClient(goserver.UDP, serverConn.LocalAddr().String(), test.options...)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Connect(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := client.Send(context.Background(), []byte("register")); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 32)
			_, remoteAddr, err := serverConn.ReadFromUDP(buffer)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := serverConn.WriteToUDP(make([]byte, test.size), remoteAddr); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			response, receiveErr := client.Receive(ctx)
			cancel()
			if test.wantErr != nil {
				if !errors.Is(receiveErr, test.wantErr) {
					t.Fatalf("Receive error = %v", receiveErr)
				}
				return
			}
			if receiveErr != nil || len(response) != test.size {
				t.Fatalf("response size = %d, error = %v", len(response), receiveErr)
			}
		})
	}
}

// TestSimpleClientUDPRawReceiveReusesBuffer verifies reuse without exposing internal storage.
func TestSimpleClientUDPRawReceiveReusesBuffer(t *testing.T) {
	serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()
	client, err := NewSimpleClient(goserver.UDP, serverConn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Send(context.Background(), []byte("register")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	_, remoteAddr, err := serverConn.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	var responses [][]byte
	var readBuffer *byte
	for index, packet := range [][]byte{[]byte("first"), []byte("other")} {
		if _, err := serverConn.WriteToUDP(packet, remoteAddr); err != nil {
			t.Fatal(err)
		}
		response, err := client.Receive(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
		if len(client.udpReadBuffer) == 0 {
			t.Fatal("UDP read buffer was not initialized")
		}
		if index == 0 {
			readBuffer = &client.udpReadBuffer[0]
		} else if readBuffer != &client.udpReadBuffer[0] {
			t.Fatal("UDP read buffer was replaced between Receive calls")
		}
	}
	if string(responses[0]) != "first" || string(responses[1]) != "other" {
		t.Fatalf("responses = %q", responses)
	}
}

// BenchmarkSimpleClientUDPReceive measures the client UDP round-trip receive path.
func BenchmarkSimpleClientUDPReceive(b *testing.B) {
	serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		b.Fatal(err)
	}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		buffer := make([]byte, 64)
		for {
			size, remoteAddr, readErr := serverConn.ReadFromUDP(buffer)
			if readErr != nil {
				return
			}
			if _, writeErr := serverConn.WriteToUDP(buffer[:size], remoteAddr); writeErr != nil {
				return
			}
		}
	}()
	defer func() {
		_ = serverConn.Close()
		<-serverDone
	}()

	client, err := NewSimpleClient(goserver.UDP, serverConn.LocalAddr().String())
	if err != nil {
		b.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	request := make([]byte, 64)
	b.SetBytes(int64(len(request)))
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if err := client.Send(context.Background(), request); err != nil {
			b.Fatal(err)
		}
		response, err := client.Receive(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if len(response) != len(request) {
			b.Fatalf("response size = %d, want %d", len(response), len(request))
		}
	}
}

// TestFrameReadersSupportFinalToken verifies compatibility with bufio.Scanner splitters.
func TestFrameReadersSupportFinalToken(t *testing.T) {
	split := func(data []byte, _ bool) (int, []byte, error) {
		if len(data) < 4 {
			return 0, nil, nil
		}
		return 0, data[:4], bufio.ErrFinalToken
	}
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	go func() { _, _ = serverConn.Write([]byte("done")) }()
	reader := newFrameReader(split, 4, 16)
	frame, err := reader.Read(clientConn)
	if err != nil || string(frame) != "done" {
		t.Fatalf("TCP final token = %q, %v", frame, err)
	}
	if _, err := reader.Read(clientConn); !errors.Is(err, io.EOF) {
		t.Fatalf("TCP read after final token = %v", err)
	}
	frames, err := splitUDPDatagram([]byte("done"), split)
	if err != nil || len(frames) != 1 || string(frames[0]) != "done" {
		t.Fatalf("UDP final tokens = %q, %v", frames, err)
	}
}

// TestSimpleClientConnAndCloseConcurrent verifies lifecycle snapshots are race-free.
func TestSimpleClientConnAndCloseConcurrent(t *testing.T) {
	_, address := startClientTestServer(t, func(context.Context, *goserver.AppSession, []byte) ([]byte, error) {
		return nil, nil
	})
	client, err := NewSimpleClient(goserver.TCP, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	var waitGroup sync.WaitGroup
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		for index := 0; index < 10000; index++ {
			_ = client.Conn()
		}
	}()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	waitGroup.Wait()
}

// TestSimpleClientTLSConnect verifies the TLS option performs a handshake.
func TestSimpleClientTLSConnect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "https://")
	client, err := NewSimpleClient(goserver.TCP, address, WithTLS(server.Client().Transport.(*http.Transport).TLSClientConfig))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.Conn() == nil {
		t.Fatal("TLS connection is nil")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestBeginEndMarkClientRoundTrip verifies framed action requests and responses.
func TestBeginEndMarkClientRoundTrip(t *testing.T) {
	receiveFilter := &filter.BeginEndMarkReceiveFilter{Begin: []byte("!$"), End: []byte("$!")}
	server, err := goserver.NewTCP("127.0.0.1:0", goserver.WithIdleTimeout(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetReceiveFilter(receiveFilter); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterSendPacketFilter(goserver.Middlewares{
		func(_ context.Context, _ *goserver.AppSession, data []byte) ([]byte, error) {
			packet := append([]byte(nil), receiveFilter.Begin...)
			packet = append(packet, data...)
			return append(packet, receiveFilter.End...), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.Action("/echo", func(_ context.Context, _ *goserver.AppSession, data []byte) ([]byte, error) {
		return data, nil
	}); err != nil {
		t.Fatal(err)
	}
	address := serveClientTestServer(t, server)
	client, err := NewBeginEndMarkClient(goserver.TCP, address, receiveFilter)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SendAction(context.Background(), "/echo", []byte("value")); err != nil {
		t.Fatal(err)
	}
	response, err := client.Receive(context.Background())
	if err != nil || string(response) != "value" {
		t.Fatalf("response = %q, %v", response, err)
	}
}

// startClientTestServer creates and serves a basic TCP server.
func startClientTestServer(t *testing.T, action goserver.ActionFunc) (*goserver.Server, string) {
	t.Helper()
	server, err := goserver.NewTCP("127.0.0.1:0", goserver.WithIdleTimeout(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetOnMessage(action); err != nil {
		t.Fatal(err)
	}
	return server, serveClientTestServer(t, server)
}

// serveClientTestServer serves server and returns its bound address.
func serveClientTestServer(t *testing.T, server *goserver.Server) string {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if address := server.Addr(); address != nil {
			return address.String()
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timeout waiting for server address")
	return ""
}
