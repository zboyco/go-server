package main

import (
	"context"
	"log"
	"time"

	goserver "github.com/zboyco/go-server/v2"
)

// runShutdownDemo 演示 Addr() 随机端口与 Shutdown(ctx) 可控停机。
func runShutdownDemo() result {
	return runDemo("shutdown with context and addr", func(t *demoT) {
		server := newTCPServer(t, "127.0.0.1", 0, goserver.WithIdleTimeout(0))
		must(t, server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, token []byte) ([]byte, error) {
			return token, nil
		}))

		done := make(chan struct{})
		go func() {
			_ = server.Serve()
			close(done)
		}()

		addr := waitAddr(t, server)
		if addr == nil {
			t.Fatalf("Addr() returned nil after start")
		}
		log.Printf("[shutdown] listen %s", addr.String())

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		must(t, server.Shutdown(ctx))
		waitServerExit(t, done)

		// 停止后再次 Shutdown 应幂等成功。
		must(t, server.Shutdown(context.Background()))
		if server.Addr() != nil {
			t.Fatalf("Addr after Shutdown should be nil")
		}
	})
}

// runRuntimeConfigGuardDemo 演示运行中修改配置会被拒绝。
func runRuntimeConfigGuardDemo() result {
	return runDemo("runtime config guard", func(t *demoT) {
		server := newTCPServer(t, "127.0.0.1", 0, goserver.WithIdleTimeout(0))
		must(t, server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, _ []byte) ([]byte, error) {
			return nil, nil
		}))

		addr, done := startServer(t, server)
		_ = addr

		// 运行中修改应返回 ErrServerRunning。
		if err := server.SetOnMessage(func(_ actionContext, _ *goserver.AppSession, _ []byte) ([]byte, error) {
			return nil, nil
		}); err != goserver.ErrServerRunning {
			t.Fatalf("SetOnMessage while running error = %v, want %v", err, goserver.ErrServerRunning)
		}
		if err := server.Action("/x", func(_ actionContext, _ *goserver.AppSession, _ []byte) ([]byte, error) {
			return nil, nil
		}); err != goserver.ErrServerRunning {
			t.Fatalf("Action while running error = %v, want %v", err, goserver.ErrServerRunning)
		}
		if err := server.SetEOF([]byte("x")); err != goserver.ErrServerRunning {
			t.Fatalf("SetEOF while running error = %v, want %v", err, goserver.ErrServerRunning)
		}

		must(t, server.Close())
		waitServerExit(t, done)
		log.Printf("[runtime-guard] rejected runtime config changes as expected")
	})
}
