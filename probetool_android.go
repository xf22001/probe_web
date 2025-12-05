package main // <--- 保持 package main，与 main.go 在同一目录时需要

import (
	"log"
	"sync"
	"time"

	// 导入核心库。导入路径是模块路径 + 核心库所在子目录。
	// 由于lib/probetool.go的包名是`probetool`，我们给导入的包一个同名别名，方便使用。
	probetool "probetool/lib" // <--- 导入路径和别名
)

// androidGlobalServerState is the single instance of probetool.ServerState
// managed by this Android binding package.
var androidGlobalServerState *probetool.ServerState // <--- 使用 probetool.ServerState
var androidGlobalServerStateMutex sync.Mutex        // Protects access to androidGlobalServerState

// Start initializes and starts the Go backend services.
// It sets up all necessary components like log server, FTP, HTTP/WS servers, and scanner.
// This function must only be called once.
// logDir: Absolute path to the directory for Go logs.
// ftpRootDir: Absolute path for the FTP server's root directory.
// staticDir: Absolute path to the directory for static HTTP files.
// timezone: Current timezone ID from Android (e.g., "Asia/Shanghai").
//
//export Start
func Start(logDir, ftpRootDir, staticDir, timezone string) {
	androidGlobalServerStateMutex.Lock()
	defer androidGlobalServerStateMutex.Unlock()

	if androidGlobalServerState != nil {
		log.Println("Probe Tool Service is already running, ignoring Start call.")
		return
	}

	// 1. Set global timezone for Go's time package
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Printf("Warning: Could not load timezone %s, defaulting to UTC: %v", timezone, err)
		loc = time.UTC
	}
	time.Local = loc // Set default local timezone for all Go code

	// 2. Initialize ServerState with provided paths
	androidGlobalServerState = probetool.NewServerState(logDir, ftpRootDir, staticDir, timezone) // <--- 使用 probetool.NewServerState

	// 3. Start core services
	if err := androidGlobalServerState.StartLogServer(); err != nil { // <--- 通过实例调用方法
		log.Printf("Failed to start log server: %v", err)
		// On critical failure, attempt to stop everything and clear state.
		androidGlobalServerState.StopFTPServer()        // <--- 通过实例调用方法
		androidGlobalServerState.StopHTTPAndWSServers() // <--- 通过实例调用方法
		androidGlobalServerState = nil
		return
	}
	androidGlobalServerState.StartFTPServer()        // <--- 通过实例调用方法
	androidGlobalServerState.StartHTTPAndWSServers() // <--- 通过实例调用方法
	go androidGlobalServerState.PerformTimedScan()   // <--- 通过实例调用方法

	log.Printf("Probe Tool Service started successfully. LogDir: %s, FTPRoot: %s, StaticDir: %s, Timezone: %s", logDir, ftpRootDir, staticDir, timezone)
}

// Stop gracefully shuts down all Go backend services.
// This function must only be called after Start, and only once per started service.
//
//export Stop
func Stop() {
	androidGlobalServerStateMutex.Lock()
	defer androidGlobalServerStateMutex.Unlock()

	if androidGlobalServerState == nil {
		log.Println("Probe Tool Service is not running, ignoring Stop call.")
		return
	}

	log.Println("Shutting down Probe Tool Service from Android...")

	// Graceful shutdown sequence
	androidGlobalServerState.StopContinuousScanner() // <--- 通过实例调用方法
	androidGlobalServerState.StopLogServer()         // <--- 通过实例调用方法
	androidGlobalServerState.StopFTPServer()         // <--- 通过实例调用方法
	androidGlobalServerState.StopHTTPAndWSServers()  // <--- 通过实例调用方法

	// Clear the global instance after shutdown
	androidGlobalServerState = nil
	log.Println("Probe Tool Service stopped successfully.")
}

// main is required by gomobile bind, but is empty as the service is controlled via Start/Stop exports.
func main() {}
