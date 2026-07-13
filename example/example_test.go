package main

import "testing"

// TestFeatureDemos 执行与命令行示例相同的场景，防止示例只编译却从未运行。
func TestFeatureDemos(t *testing.T) {
	for _, item := range runAllDemos() {
		item := item
		t.Run(item.name, func(t *testing.T) {
			if item.skipReason != "" {
				t.Skip(item.skipReason)
			}
			if item.err != nil {
				t.Fatal(item.err)
			}
		})
	}
}
