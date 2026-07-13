// Package main 提供 go-server 的完整特性演示。
//
// 运行：
//
//	go run ./example
//
// 该示例会依次启动服务端与客户端，覆盖 TCP/UDP/TLS、IPv6、路由、
// 中间件、自定义协议、协议错误、会话生命周期、超时和服务关闭等能力。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("go-server feature demo")
	fmt.Println("======================")

	results := runAllDemos()

	fmt.Println()
	fmt.Println("summary")
	fmt.Println("-------")

	failed := 0
	for _, item := range results {
		status := "PASS"
		if item.skipReason != "" {
			status = "SKIP"
		}
		if item.err != nil {
			status = "FAIL"
			failed++
		}
		fmt.Printf("[%s] %s\n", status, item.name)
		switch {
		case item.err != nil:
			fmt.Printf("       %v\n", item.err)
		case item.skipReason != "":
			fmt.Printf("       %s\n", item.skipReason)
		}
	}

	if failed > 0 {
		fmt.Printf("\n%d demo(s) failed\n", failed)
		os.Exit(1)
	}
	fmt.Println("\nall runnable demos passed")
}

// runAllDemos 依次执行全部功能演示并返回可汇总的结果。
func runAllDemos() []result {
	return []result{
		runTCPBeginEndDemo(),
		runTCPOnMessageDemo(),
		runUDPBeginEndDemo(),
		runFixedHeaderDemo(),
		runCustomSplitDemo(),
		runCustomReceiveFilterDemo(),
		runProtocolErrorDemo(),
		runUDPConnectionFilterDemo(),
		runIdleTimeoutDemo(),
		runIPv6Demo(),
		runSessionLifecycleDemo(),
		runTLSDemo(),
		runShutdownDemo(),
		runRuntimeConfigGuardDemo(),
	}
}
