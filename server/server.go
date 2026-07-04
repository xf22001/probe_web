package server

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	// 导入核心库，路径是模块名/lib
	probetoollib "probetool/lib" // <--- 修改导入路径和别名
)

// androidGlobalServerState is the single instance of probetoollib.ServerState
// managed by this Android binding package.
var androidGlobalServerState *probetoollib.ServerState
var androidGlobalServerStateMutex sync.Mutex // Protects access to androidGlobalServerState
var androidGlobalLastError string

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

	if androidGlobalServerState != nil && androidGlobalServerState.IsRunning() {
		log.Println("Probe Tool Service is already running, ignoring Start call.")
		return
	}
	androidGlobalLastError = ""

	// Set global timezone for Go's time package
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Printf("Warning: Could not load timezone %s, defaulting to UTC: %v", timezone, err)
		loc = time.UTC
	}
	time.Local = loc // Set default local timezone for all Go code

	// Initialize ServerState with provided paths
	state := probetoollib.NewServerState(logDir, ftpRootDir, staticDir, timezone)

	// 在日志重定向之前，把启动参数打到控制台（不进日志文件）
	fmt.Fprintf(os.Stderr, "Probe Tool Service starting\n")
	fmt.Fprintf(os.Stderr, "  Log dir:    %s\n", logDir)
	fmt.Fprintf(os.Stderr, "  FTP root:   %s\n", ftpRootDir)
	fmt.Fprintf(os.Stderr, "  Static dir: %s\n", staticDir)
	fmt.Fprintf(os.Stderr, "  Timezone:   %s\n", timezone)

	if err := state.StartCoreServices(); err != nil {
		androidGlobalLastError = err.Error()
		log.Printf("Failed to start Probe Tool Service: %v", err)
		return
	}

	androidGlobalServerState = state
	go state.PerformTimedScan()

	log.Printf("Probe Tool Service started successfully.")
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
	androidGlobalServerState.StopCoreServices()

	// Clear the global instance after shutdown
	androidGlobalServerState = nil
	log.Println("Probe Tool Service stopped successfully.")
}

// IsRunning reports whether the Go backend services are actually running.
//
//export IsRunning
func IsRunning() bool {
	androidGlobalServerStateMutex.Lock()
	defer androidGlobalServerStateMutex.Unlock()

	return androidGlobalServerState != nil && androidGlobalServerState.IsRunning()
}

// LastError returns the latest startup or shutdown error from the Go backend.
//
//export LastError
func LastError() string {
	androidGlobalServerStateMutex.Lock()
	defer androidGlobalServerStateMutex.Unlock()

	if androidGlobalServerState != nil {
		if message := androidGlobalServerState.LastError(); message != "" {
			return message
		}
	}
	return androidGlobalLastError
}
