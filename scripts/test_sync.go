package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Weknora/Weknora/internal/container"
)

func main() {
	ctx := context.Background()

	// 初始化容器
	c := container.GetContainer()

	// 数据源 ID
	dsID := "4032928e-89e2-443d-ab11-63de450a6263"

	fmt.Printf("🔄 开始同步数据源: %s\n", dsID)

	// 调用 ManualSync
	syncLog, err := c.DataSourceService.ManualSync(ctx, dsID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 同步失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ 同步已触发!\n")
	fmt.Printf("📋 同步日志 ID: %s\n", syncLog.ID)
	fmt.Printf("📊 状态: %s\n", syncLog.Status)
	fmt.Printf("🕐 开始时间: %s\n", syncLog.StartedAt)
}
