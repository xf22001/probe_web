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
	probetool "probetool/lib"
)

// serverState 是指向 probetool.ServerState 实例的全局变量，用于桌面应用。
var serverState *probetool.ServerState

func main() {
	// 设置主应用程序的日志输出 (初始为标准错误/输出)
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting Probe Tool Desktop Application...")

	// 定义桌面版本的路径
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}
	baseDir := filepath.Dir(exePath)
	logDir := filepath.Join(baseDir, "logs")
	ftpRootDir := filepath.Join(baseDir, "ftp_share")
	staticDir := filepath.Join(baseDir, "static") // 假设 'static' 文件夹与可执行文件在同一目录

	// 确保目录存在
	for _, dir := range []string{logDir, ftpRootDir, staticDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("Failed to create directory %s: %v", dir, err)
		}
	}

	// 创建并初始化 ServerState
	serverState = probetool.NewServerState(logDir, ftpRootDir, staticDir, "Local")

	// 在日志重定向之前，把启动参数打到控制台（不进日志文件）
	url := fmt.Sprintf("http://127.0.0.1:%d", probetool.HTTPPort)
	fmt.Fprintf(os.Stderr, "Probe Web Tool started\n")
	fmt.Fprintf(os.Stderr, "  Log dir:    %s\n", logDir)
	fmt.Fprintf(os.Stderr, "  FTP root:   %s\n", ftpRootDir)
	fmt.Fprintf(os.Stderr, "  Static dir: %s\n", staticDir)
	fmt.Fprintf(os.Stderr, "  Timezone:   Local\n")
	fmt.Fprintf(os.Stderr, "  URL:        %s\n", url)
	fmt.Fprintf(os.Stderr, "  FTP:        ftp://127.0.0.1:%d\n", probetool.FTPPort)
	fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop.\n")

	// 启动所有核心服务
	if err := serverState.StartCoreServices(); err != nil {
		log.Fatalf("Failed to start core services: %v", err)
	}
	go serverState.PerformTimedScan()

	// 自动打开浏览器
	go func() {
		time.Sleep(1 * time.Second)
		if err := browser.OpenURL(url); err != nil {
			log.Printf("Failed to open browser: %v", err)
		}
	}()

	// 等待关闭信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("Shutting down Probe Tool Desktop Application...")

	// 优雅关闭序列
	serverState.StopCoreServices()

	log.Println("Application exited gracefully.")
}
