package main

import (
	// Import context package for graceful shutdown
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pkg/browser" // For opening browser

	// 导入核心库。导入路径是模块路径 + 核心库所在子目录。
	// 由于lib/probetool.go的包名是`probetool`，我们给导入的包一个同名别名，方便使用。
	probetool "probetool/lib" // <--- 导入路径和别名
)

// serverState 是指向 probetool.ServerState 实例的全局变量，用于桌面应用。
var serverState *probetool.ServerState

func main() {
	// 1. 设置主应用程序的日志输出 (初始为标准错误/输出)
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting Probe Tool Desktop Application...")

	// 2. 定义桌面版本的路径
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}
	baseDir := filepath.Dir(exePath)
	logDir := filepath.Join(baseDir, "logs")
	ftpRootDir := filepath.Join(baseDir, "ftp_share")
	staticDir := filepath.Join(baseDir, "static") // 假设 'static' 文件夹与可执行文件在同一目录

	// 确保目录存在
	os.MkdirAll(logDir, 0755)
	os.MkdirAll(ftpRootDir, 0755)
	os.MkdirAll(staticDir, 0755)

	// 3. 为主应用程序创建并初始化 ServerState
	serverState = probetool.NewServerState(logDir, ftpRootDir, staticDir, "Local") // 使用 probetool.NewServerState

	// 4. 启动所有核心服务
	if err := serverState.StartLogServer(); err != nil { // 通过实例调用方法
		log.Fatalf("Failed to start log server: %v", err)
	}
	serverState.StartFTPServer()        // 通过实例调用方法
	serverState.StartHTTPAndWSServers() // 通过实例调用方法
	go serverState.PerformTimedScan()   // 通过实例调用方法

	// 5. 自动打开浏览器
	go func() {
		time.Sleep(1 * time.Second)                                   // 等待服务器启动
		url := fmt.Sprintf("http://127.0.0.1:%d", probetool.HTTPPort) // 使用 probetool.HTTPPort
		log.Printf("Opening browser to %s", url)
		if err := browser.OpenURL(url); err != nil {
			log.Printf("Failed to open browser: %v", err)
		}
	}()

	// 6. 设置操作系统信号的优雅关闭
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Desktop application running. Press Ctrl+C to stop.")
	<-sigChan // 阻塞直到接收到信号
	log.Println("Shutting down Probe Tool Desktop Application...")

	// 7. 优雅关闭序列
	serverState.StopContinuousScanner()
	serverState.StopLogServer()
	serverState.StopFTPServer()
	serverState.StopHTTPAndWSServers()

	log.Println("Application exited gracefully.")
}
