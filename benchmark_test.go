package goserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/zboyco/go-server/filter"
)

// BenchmarkCountSessions measures active-count reads while many closing sessions remain internally resident.
func BenchmarkCountSessions(b *testing.B) {
	const residentSessions = 10_000
	var server Server
	for index := 0; index < residentSessions; index++ {
		session := &AppSession{ID: fmt.Sprintf("session-%d", index)}
		if stored, created := server.sessions.store(session); !created || stored != session {
			b.Fatalf("store session %d = (%p, %v)", index, stored, created)
		}
		if index%2 == 0 {
			session.interrupt()
		}
	}
	b.Cleanup(server.sessions.clear)
	want := residentSessions / 2
	if got := server.CountSessions(); got != want {
		b.Fatalf("active sessions = %d, want %d", got, want)
	}
	b.ReportAllocs()
	b.ReportMetric(residentSessions, "resident_sessions")
	b.ResetTimer()
	count := 0
	for index := 0; index < b.N; index++ {
		count = server.CountSessions()
	}
	if count != want {
		b.Fatalf("active sessions after benchmark = %d, want %d", count, want)
	}
}

// BenchmarkTCPEcho compares the library with an equivalent standard-library loop.
func BenchmarkTCPEcho(b *testing.B) {
	implementations := []struct {
		name  string
		start func(testing.TB) net.Addr
	}{
		{name: "go-server", start: startBenchmarkGoServerTCP},
		{name: "stdlib", start: startBenchmarkStdlibTCP},
	}
	for _, implementation := range implementations {
		implementation := implementation
		for _, size := range []int{64, 1024} {
			size := size
			b.Run(fmt.Sprintf("%s/serial/%dB", implementation.name, size), func(b *testing.B) {
				runTCPBenchmark(b, implementation.start(b), size, false)
			})
		}
		b.Run(implementation.name+"/parallel/64B", func(b *testing.B) {
			runTCPBenchmark(b, implementation.start(b), 64, true)
		})
	}
}

// BenchmarkTCPFixedHeaderRoute measures framing and action-routing overhead.
func BenchmarkTCPFixedHeaderRoute(b *testing.B) {
	for _, size := range []int{64, 1024} {
		size := size
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			server, err := NewTCP("127.0.0.1:0", WithIdleTimeout(0))
			if err != nil {
				b.Fatal(err)
			}
			if err := server.SetReceiveFilter(&filter.FixedHeaderReceiveFilter{}); err != nil {
				b.Fatal(err)
			}
			if err := server.Action("/echo", func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
				return token, nil
			}); err != nil {
				b.Fatal(err)
			}
			address := startBenchmarkServer(b, server)
			conn, err := net.Dial("tcp", address.String())
			if err != nil {
				b.Fatal(err)
			}
			defer conn.Close()
			action := []byte("/echo")
			request := make([]byte, 8+len(action)+size)
			binary.BigEndian.PutUint32(request[:4], uint32(len(request)))
			binary.BigEndian.PutUint32(request[4:8], uint32(len(action)))
			copy(request[8:], action)
			copy(request[8+len(action):], bytes.Repeat([]byte{'x'}, size))
			response := make([]byte, size)
			benchmarkRoundTrip(b, conn, request, response)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				benchmarkRoundTrip(b, conn, request, response)
			}
		})
	}
}

// BenchmarkUDPEcho compares queued, inline, and direct UDP echo paths.
func BenchmarkUDPEcho(b *testing.B) {
	implementations := []struct {
		name  string
		start func(testing.TB) *net.UDPAddr
	}{
		{
			name: "go-server/queued",
			start: func(tb testing.TB) *net.UDPAddr {
				return startBenchmarkGoServerUDP(tb, UDPDispatchQueued, nil)
			},
		},
		{
			name: "go-server/inline",
			start: func(tb testing.TB) *net.UDPAddr {
				return startBenchmarkGoServerUDP(tb, UDPDispatchInline, nil)
			},
		},
		{name: "stdlib", start: startBenchmarkStdlibUDP},
	}
	for _, implementation := range implementations {
		implementation := implementation
		for _, size := range []int{64, 1024, 8 * 1024} {
			size := size
			b.Run(fmt.Sprintf("%s/serial/%dB", implementation.name, size), func(b *testing.B) {
				runUDPBenchmark(b, implementation.start(b), size)
			})
		}
		b.Run(implementation.name+"/parallel/64B", func(b *testing.B) {
			runUDPParallelBenchmark(b, implementation.start(b), 64)
		})
	}
}

// BenchmarkUDPSlowAction compares dispatch behavior when actions dominate latency.
func BenchmarkUDPSlowAction(b *testing.B) {
	action := func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
		time.Sleep(100 * time.Microsecond)
		return token, nil
	}
	for _, mode := range []UDPDispatchMode{UDPDispatchQueued, UDPDispatchInline} {
		mode := mode
		name := "queued"
		if mode == UDPDispatchInline {
			name = "inline"
		}
		b.Run(name+"/parallel/64B", func(b *testing.B) {
			address := startBenchmarkGoServerUDP(b, mode, action)
			runUDPParallelBenchmark(b, address, 64)
		})
	}
}

// BenchmarkUDPQueuedOverload measures bounded queue rejection throughput.
func BenchmarkUDPQueuedOverload(b *testing.B) {
	blocked := make(chan struct{})
	started := make(chan struct{})
	var startedOnce sync.Once
	action := func(context.Context, *AppSession, []byte) ([]byte, error) {
		startedOnce.Do(func() { close(started) })
		<-blocked
		return nil, nil
	}
	server, err := NewUDP("127.0.0.1:0",
		WithIdleTimeout(0),
		WithUDPWorkers(1),
		WithUDPQueueSize(1),
		WithUDPDispatchMode(UDPDispatchQueued),
	)
	if err != nil {
		b.Fatal(err)
	}
	if err := server.SetOnMessage(action); err != nil {
		b.Fatal(err)
	}
	address := startBenchmarkServer(b, server).(*net.UDPAddr)
	b.Cleanup(func() { close(blocked) })
	conn, err := net.DialUDP("udp", nil, address)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	request := make([]byte, 65)
	request[len(request)-1] = '\n'
	if _, err := conn.Write(request); err != nil {
		b.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		b.Fatal("timeout waiting for blocked UDP action")
	}
	if _, err := conn.Write(request); err != nil {
		b.Fatal(err)
	}
	waitBenchmarkPackets(b, server, 2)

	baseline := server.Stats()
	b.SetBytes(64)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := conn.Write(request); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	waitBenchmarkPacketDrain(server, baseline.PacketsRead)
	stats := server.Stats()
	read := stats.PacketsRead - baseline.PacketsRead
	dropped := stats.PacketsDropped - baseline.PacketsDropped
	if read > 0 {
		b.ReportMetric(float64(dropped)/float64(read), "drop/read")
	}
}

// startBenchmarkGoServerTCP starts an on-message TCP echo server.
func startBenchmarkGoServerTCP(tb testing.TB) net.Addr {
	server, err := NewTCP("127.0.0.1:0", WithIdleTimeout(0))
	if err != nil {
		tb.Fatal(err)
	}
	if err := server.SetOnMessage(func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
		return token, nil
	}); err != nil {
		tb.Fatal(err)
	}
	return startBenchmarkServer(tb, server)
}

// startBenchmarkGoServerUDP starts an echo server with one dispatch mode.
func startBenchmarkGoServerUDP(tb testing.TB, mode UDPDispatchMode, action ActionFunc) *net.UDPAddr {
	tb.Helper()
	server, err := NewUDP("127.0.0.1:0", WithIdleTimeout(0), WithUDPDispatchMode(mode))
	if err != nil {
		tb.Fatal(err)
	}
	if action == nil {
		action = func(_ context.Context, _ *AppSession, token []byte) ([]byte, error) {
			return token, nil
		}
	}
	if err := server.SetOnMessage(action); err != nil {
		tb.Fatal(err)
	}
	return startBenchmarkServer(tb, server).(*net.UDPAddr)
}

// startBenchmarkServer starts a library server and registers cleanup.
func startBenchmarkServer(tb testing.TB, server *Server) net.Addr {
	tb.Helper()
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	tb.Cleanup(func() {
		_ = server.Close()
		<-done
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if address := server.Addr(); address != nil {
			return address
		}
		time.Sleep(time.Millisecond)
	}
	tb.Fatal("timeout waiting for benchmark server")
	return nil
}

// startBenchmarkStdlibTCP starts an equivalent Scanner-based TCP server.
func startBenchmarkStdlibTCP(tb testing.TB) net.Addr {
	tb.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	var handlers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					if _, err := conn.Write(scanner.Bytes()); err != nil {
						return
					}
				}
			}()
		}
	}()
	tb.Cleanup(func() {
		_ = listener.Close()
		<-done
		handlers.Wait()
	})
	return listener.Addr()
}

// runTCPBenchmark runs serial or testing.RunParallel request/response loops.
func runTCPBenchmark(b *testing.B, address net.Addr, size int, parallel bool) {
	b.Helper()
	payload := bytes.Repeat([]byte{'x'}, size)
	request := append(append([]byte(nil), payload...), '\n')
	b.SetBytes(int64(size))
	b.ReportAllocs()
	if parallel {
		b.ResetTimer()
		b.RunParallel(func(iterations *testing.PB) {
			conn, err := net.Dial("tcp", address.String())
			if err != nil {
				b.Error(err)
				return
			}
			defer conn.Close()
			response := make([]byte, size)
			for iterations.Next() {
				benchmarkRoundTrip(b, conn, request, response)
			}
		})
		return
	}
	conn, err := net.Dial("tcp", address.String())
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	response := make([]byte, size)
	benchmarkRoundTrip(b, conn, request, response)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		benchmarkRoundTrip(b, conn, request, response)
	}
}

// benchmarkRoundTrip writes one request and reads one fixed-size response.
func benchmarkRoundTrip(b *testing.B, conn net.Conn, request, response []byte) {
	b.Helper()
	if _, err := conn.Write(request); err != nil {
		b.Fatal(err)
	}
	if _, err := io.ReadFull(conn, response); err != nil {
		b.Fatal(err)
	}
}

// startBenchmarkStdlibUDP starts a direct UDP echo loop.
func startBenchmarkStdlibUDP(tb testing.TB) *net.UDPAddr {
	tb.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		tb.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 64*1024)
		for {
			size, address, readErr := conn.ReadFromUDP(buffer)
			if readErr != nil {
				return
			}
			if size > 0 && buffer[size-1] == '\n' {
				size--
			}
			_, _ = conn.WriteToUDP(buffer[:size], address)
		}
	}()
	tb.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	return conn.LocalAddr().(*net.UDPAddr)
}

// runUDPBenchmark runs one connected UDP request/response loop.
func runUDPBenchmark(b *testing.B, address *net.UDPAddr, size int) {
	b.Helper()
	conn, err := net.DialUDP("udp", nil, address)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	request := append(bytes.Repeat([]byte{'x'}, size), '\n')
	response := make([]byte, size)
	benchmarkRoundTrip(b, conn, request, response)
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		benchmarkRoundTrip(b, conn, request, response)
	}
}

// runUDPParallelBenchmark runs request/response loops from independent endpoints.
func runUDPParallelBenchmark(b *testing.B, address *net.UDPAddr, size int) {
	b.Helper()
	request := append(bytes.Repeat([]byte{'x'}, size), '\n')
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(iterations *testing.PB) {
		conn, err := net.DialUDP("udp", nil, address)
		if err != nil {
			b.Error(err)
			return
		}
		defer conn.Close()
		response := make([]byte, size)
		for iterations.Next() {
			benchmarkRoundTrip(b, conn, request, response)
		}
	})
}

// waitBenchmarkPackets waits until the server accounts for a target packet count.
func waitBenchmarkPackets(b *testing.B, server *Server, target uint64) {
	b.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if server.Stats().PacketsRead >= target {
			return
		}
		time.Sleep(time.Millisecond)
	}
	b.Fatalf("packets read = %d, want at least %d", server.Stats().PacketsRead, target)
}

// waitBenchmarkPacketDrain waits for packet accounting to settle after a send burst.
func waitBenchmarkPacketDrain(server *Server, baseline uint64) {
	deadline := time.Now().Add(time.Second)
	lastRead := baseline
	stableChecks := 0
	for time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		current := server.Stats().PacketsRead
		if current > baseline && current == lastRead {
			stableChecks++
			if stableChecks == 10 {
				return
			}
		} else {
			stableChecks = 0
		}
		lastRead = current
	}
}
