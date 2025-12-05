package server

import (
	"log"
	"sync"
	"time"

	// 导入核心库，路径是模块名/lib
	probetoollib "probetool/lib" // <--- 修改导入路径和别名
)

// androidGlobalServerState is the single instance of probetoollib.ServerState
// managed by this Android binding package.
var androidGlobalServerState *probetoollib.ServerState // <--- 使用 probetoollib.ServerState
var androidGlobalServerStateMutex sync.Mutex           // Protects access to androidGlobalServerState

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
	androidGlobalServerState = probetoollib.NewServerState(logDir, ftpRootDir, staticDir, timezone) // <--- 使用 probetoollib.NewServerState

	// 3. Start core services
	if err := androidGlobalServerState.StartLogServer(); err != nil { // <--- 通过实例调用方法
		log.Printf("Failed to start log server: %v", err)
		androidGlobalServerState.StopFTPServer()
		androidGlobalServerState.StopHTTPAndWSServers()
		androidGlobalServerState = nil
		return
	}
	androidGlobalServerState.StartFTPServer()
	androidGlobalServerState.StartHTTPAndWSServers()
	go androidGlobalServerState.PerformTimedScan()

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
	androidGlobalServerState.StopContinuousScanner()
	androidGlobalServerState.StopLogServer()
	androidGlobalServerState.StopFTPServer()
	androidGlobalServerState.StopHTTPAndWSServers()

	// Clear the global instance after shutdown
	androidGlobalServerState = nil
	log.Println("Probe Tool Service stopped successfully.")
}
