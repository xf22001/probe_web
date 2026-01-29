package probetool

import (
	"bytes"
	"context" // For graceful HTTP server shutdown
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/gorilla/websocket"
	"github.com/spf13/afero" // For FTP server filesystem abstraction
)

// ==========================================
// 1. Configuration & Constants
// ==========================================

const (
	HTTPPort      = 8000
	WSPort        = 8001
	BroadcastPort = 6000
	ProbeToolPort = 6001
	LogToolPort   = 6002
	FTPPort       = 2121
	// LogDir, FTPRootDir, StaticDir will be passed to ServerState constructor.

	// 根据C协议客户端128字节缓冲区限制，以及RequestTSize = 25字节（header + payload_info）。
	// MaxFragmentPayloadSize = 128 (客户端缓冲区) - RequestTSize (协议头部大小) = 128 - 25 = 103 字节。
	// 这表示单个UDP包最多能携带103字节的实际数据。
	MaxFragmentPayloadSize = 103
)

// ==========================================
// 2. Protocol Logic (CRC & Packet)
// ==========================================

const (
	DefaultRequestMagic = 0xA5A55A5A
	HeaderSize          = 17                           // magic(4) + total_size(4) + data_size(4) + data_offset(4) + crc(1)
	PayloadInfoSize     = 8                            // fn(4) + stage(4)
	RequestTSize        = HeaderSize + PayloadInfoSize // 25 bytes total protocol header
)

func CalcCRC8(data []byte) uint8 {
	var crc uint8 = 0
	for _, b := range data {
		crc = (crc + b) & 0xFF
	}
	return crc
}

// encodeHeader encodes the packet header efficiently
func encodeHeader(magic, totalSize, dataSize, dataOffset uint32, crc uint8) []byte {
	header := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint32(header[0:4], magic)
	binary.LittleEndian.PutUint32(header[4:8], totalSize)
	binary.LittleEndian.PutUint32(header[8:12], dataSize)
	binary.LittleEndian.PutUint32(header[12:16], dataOffset)
	header[16] = crc
	return header
}

// encodePayloadInfo encodes the payload info efficiently
func encodePayloadInfo(fn, stage uint32) []byte {
	payloadInfo := make([]byte, PayloadInfoSize)
	binary.LittleEndian.PutUint32(payloadInfo[0:4], fn)
	binary.LittleEndian.PutUint32(payloadInfo[4:8], stage)
	return payloadInfo
}

// EncodeRequest now returns a slice of byte slices, representing multiple packets if fragmentation is needed.
func EncodeRequest(fn uint32, stage uint32, data []byte) [][]byte {
	var packets [][]byte
	totalDataSize := uint32(len(data))

	// If data is empty or fits within a single fragment payload, send it as one packet.
	if totalDataSize == 0 || totalDataSize <= MaxFragmentPayloadSize {
		payloadBytes := encodePayloadInfo(fn, stage)
		crcData := append(payloadBytes, data...)
		crc := CalcCRC8(crcData)

		header := encodeHeader(DefaultRequestMagic, totalDataSize, totalDataSize, 0, crc)

		packet := make([]byte, 0, len(header)+len(payloadBytes)+len(data))
		packet = append(packet, header...)
		packet = append(packet, payloadBytes...)
		packet = append(packet, data...)

		packets = append(packets, packet)
		return packets
	}

	// Fragmentation is needed
	var offset uint32 = 0
	payloadBytes := encodePayloadInfo(fn, stage)

	for offset < totalDataSize {
		currentFragmentSize := uint32(MaxFragmentPayloadSize)
		if offset+currentFragmentSize > totalDataSize {
			currentFragmentSize = totalDataSize - offset
		}

		fragmentData := data[offset : offset+currentFragmentSize]

		crcData := append(payloadBytes, fragmentData...)
		crc := CalcCRC8(crcData)

		header := encodeHeader(DefaultRequestMagic, totalDataSize, currentFragmentSize, offset, crc)

		packet := make([]byte, 0, len(header)+len(payloadBytes)+len(fragmentData))
		packet = append(packet, header...)
		packet = append(packet, payloadBytes...)
		packet = append(packet, fragmentData...)

		packets = append(packets, packet)

		offset += currentFragmentSize
	}
	return packets
}

// DecodeRequest now returns additional header fields: totalSize, dataSize, dataOffset.
func DecodeRequest(buffer []byte) (map[string]interface{}, uint32, uint32, uint32, []byte, error) {
	if len(buffer) < RequestTSize {
		return nil, 0, 0, 0, nil, fmt.Errorf("incomplete buffer, length %d < %d", len(buffer), RequestTSize)
	}

	headerBytes := buffer[:HeaderSize]
	payloadInfoBytes := buffer[HeaderSize:RequestTSize]

	var magic, totalSize, dataSize, dataOffset uint32
	var crcReceived uint8

	// Use a single reader for efficiency
	reader := bytes.NewReader(headerBytes)
	if err := binary.Read(reader, binary.LittleEndian, &magic); err != nil {
		return nil, 0, 0, 0, nil, fmt.Errorf("failed to read magic: %v", err)
	}
	if err := binary.Read(reader, binary.LittleEndian, &totalSize); err != nil {
		return nil, 0, 0, 0, nil, fmt.Errorf("failed to read totalSize: %v", err)
	}
	if err := binary.Read(reader, binary.LittleEndian, &dataSize); err != nil {
		return nil, 0, 0, 0, nil, fmt.Errorf("failed to read dataSize: %v", err)
	}
	if err := binary.Read(reader, binary.LittleEndian, &dataOffset); err != nil {
		return nil, 0, 0, 0, nil, fmt.Errorf("failed to read dataOffset: %v", err)
	}
	if err := binary.Read(reader, binary.LittleEndian, &crcReceived); err != nil {
		return nil, 0, 0, 0, nil, fmt.Errorf("failed to read crc: %v", err)
	}

	if magic != DefaultRequestMagic {
		return nil, 0, 0, 0, nil, fmt.Errorf("invalid magic 0x%x, expected 0x%x", magic, uint32(DefaultRequestMagic))
	}

	// Input validation to prevent potential attacks
	if dataSize > MaxFragmentPayloadSize {
		return nil, 0, 0, 0, nil, fmt.Errorf("dataSize too large: %d, max allowed: %d", dataSize, MaxFragmentPayloadSize)
	}

	if totalSize > 1024*1024 { // Limit total message size to 1MB to prevent memory exhaustion
		return nil, 0, 0, 0, nil, fmt.Errorf("totalSize too large: %d, max allowed: %d", totalSize, 1024*1024)
	}

	if len(buffer) < int(RequestTSize+dataSize) {
		return nil, 0, 0, 0, nil, fmt.Errorf("incomplete data payload (expected %d bytes, got %d in buffer after header)", dataSize, len(buffer)-RequestTSize)
	}

	if totalSize > 0 && dataOffset+dataSize > totalSize {
		return nil, 0, 0, 0, nil, fmt.Errorf("fragment range out of total bounds: offset %d, size %d, total %d", dataOffset, dataSize, totalSize)
	}

	actualDataBytes := buffer[RequestTSize : RequestTSize+int(dataSize)]
	crcData := append(payloadInfoBytes, actualDataBytes...)
	if crcReceived != CalcCRC8(crcData) {
		return nil, 0, 0, 0, nil, fmt.Errorf("invalid crc, calculated 0x%x, received 0x%x", CalcCRC8(crcData), crcReceived)
	}

	var fn, stage uint32
	payloadReader := bytes.NewReader(payloadInfoBytes)
	if err := binary.Read(payloadReader, binary.LittleEndian, &fn); err != nil {
		return nil, 0, 0, 0, nil, fmt.Errorf("failed to read fn: %v", err)
	}
	if err := binary.Read(payloadReader, binary.LittleEndian, &stage); err != nil {
		return nil, 0, 0, 0, nil, fmt.Errorf("failed to read stage: %v", err)
	}

	return map[string]interface{}{"fn": fn, "stage": stage}, totalSize, dataSize, dataOffset, actualDataBytes, nil
}

// ==========================================
// 3. Reassembly Logic
// ==========================================

// PartialMessage stores fragments of a larger message being reassembled.
type PartialMessage struct {
	Fn             uint32
	Stage          uint32
	TotalSize      uint32
	ReceivedData   []byte // The buffer to reconstruct the full message
	ReceivedMask   []byte // Bitmask to track if all segments have been received (each bit represents 1 byte)
	LastUpdateTime time.Time
	mu             sync.Mutex // Mutex to protect access to this partial message
}

// Reassembler manages reassembly buffer for a single logical message stream (per connection).
type Reassembler struct {
	currentMessage  *PartialMessage // The current message being reassembled
	mu              sync.RWMutex    // RWMutex to allow concurrent read access when no modification occurs
	timeout         time.Duration   // Timeout for incomplete messages
	cleanupTicker   *time.Ticker    // For periodic cleanup
	stopCleanupChan chan struct{}   // To stop cleanup goroutine
}

// NewReassembler creates and returns a new Reassembler instance.
// It also starts a background cleanup routine.
func NewReassembler(timeout time.Duration) *Reassembler {
	r := &Reassembler{
		currentMessage:  nil, // Initially no message is being reassembler
		timeout:         timeout,
		cleanupTicker:   time.NewTicker(timeout / 2), // Check periodically, half the timeout duration
		stopCleanupChan: make(chan struct{}),
	}
	go r.cleanupRoutine()
	return r
}

// cleanupRoutine periodically checks for and removes timed-out partial messages.
func (r *Reassembler) cleanupRoutine() {
	defer r.cleanupTicker.Stop()
	for {
		select {
		case <-r.cleanupTicker.C:
			r.mu.RLock() // Use read lock initially to check if there's a message
			currentMsg := r.currentMessage
			r.mu.RUnlock()

			if currentMsg != nil {
				currentMsg.mu.Lock() // Lock the message first
				r.mu.Lock() // Then lock the reassembler to safely access currentMessage

				// Double-check that the message hasn't changed while acquiring locks
				if r.currentMessage == currentMsg && time.Since(currentMsg.LastUpdateTime) > r.timeout {
					log.Printf("Reassembler: Cleaning up timed out partial message (fn: %d, stage: %d, totalSize: %d)",
						currentMsg.Fn, currentMsg.Stage, currentMsg.TotalSize)
					r.currentMessage = nil // Clear the timed-out message
				}

				r.mu.Unlock() // Unlock reassembler
				currentMsg.mu.Unlock() // Unlock message
			}
		case <-r.stopCleanupChan:
			return
		}
	}
}

// StopCleanup stops the Reassembler's background cleanup goroutine.
func (r *Reassembler) StopCleanup() {
	close(r.stopCleanupChan)
}

// Clear clears any currently reassembling message state.
// Used when a connection is disconnected.
func (r *Reassembler) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentMessage != nil {
		r.currentMessage.mu.Lock() // Lock to safely clear
		r.currentMessage = nil
		r.currentMessage.mu.Unlock()
		log.Println("Reassembler: Cleared current message state.")
	}
}

// Helper functions for bitmask operations
func getBit(mask []byte, index uint32) bool {
	byteIndex := index / 8
	bitIndex := index % 8
	if byteIndex >= uint32(len(mask)) {
		return false
	}
	return (mask[byteIndex] & (1 << bitIndex)) != 0
}

func setBit(mask []byte, index uint32) {
	byteIndex := index / 8
	bitIndex := index % 8
	if byteIndex >= uint32(len(mask)) {
		return
	}
	mask[byteIndex] |= (1 << bitIndex)
}

// AddFragment adds a received fragment to the reassembly buffer.
// It returns the fully reassembled message if complete, otherwise nil.
// It also returns an error if something goes wrong (e.g., mismatched totalSize for an existing message).
// The 'ip' parameter is mainly for logging context.
func (r *Reassembler) AddFragment(ip string, fn, stage, totalSize, dataSize, dataOffset uint32, fragmentData []byte) ([]byte, error) {
	r.mu.Lock() // Lock Reassembler instance
	pm := r.currentMessage
	if pm == nil {
		// This is the first fragment for a new message sequence.
		if totalSize == 0 && dataSize == 0 { // Empty message is considered immediately complete
			r.mu.Unlock()
			return []byte{}, nil
		}
		// Calculate the number of bytes needed for the bitmask
		maskSize := (totalSize + 7) / 8 // Round up to nearest byte
		pm = &PartialMessage{
			Fn:             fn,
			Stage:          stage,
			TotalSize:      totalSize,
			ReceivedData:   make([]byte, totalSize),
			ReceivedMask:   make([]byte, maskSize),
			LastUpdateTime: time.Now(),
		}
		r.currentMessage = pm // Set as the current message being reassembled
	}
	r.mu.Unlock() // Release Reassembler lock early

	pm.mu.Lock() // Lock the current PartialMessage
	defer pm.mu.Unlock()

	// Update timestamp for activity
	pm.LastUpdateTime = time.Now()

	// Consistency checks: If fn/stage/totalSize differ for an existing incomplete message,
	// it's likely a new message has started before the old one completed.
	// In a UDP context without a unique message ID per logical message, this is a reasonable heuristic.
	if (pm.Fn != fn || pm.Stage != stage || pm.TotalSize != totalSize) && pm.TotalSize != 0 {
		log.Printf("Reassembler for %s: Clearing old partial message due to new sequence. Old: fn=%d,stage=%d,total=%d. New: fn=%d,stage=%d,total=%d",
			ip, pm.Fn, pm.Stage, pm.TotalSize, fn, stage, totalSize)
		// Clear the old message and initialize for the new one
		maskSize := (totalSize + 7) / 8 // Round up to nearest byte
		pm.Fn = fn
		pm.Stage = stage
		pm.TotalSize = totalSize
		pm.ReceivedData = make([]byte, totalSize)
		pm.ReceivedMask = make([]byte, maskSize)
	} else if pm.TotalSize != totalSize {
		return nil, fmt.Errorf("reassembler for %s: total size mismatch. Existing: %d, New fragment: %d", ip, pm.TotalSize, totalSize)
	}

	// Validate fragment bounds before copying
	if int(dataOffset+dataSize) > len(pm.ReceivedData) {
		return nil, fmt.Errorf("reassembler for %s: fragment data exceeds buffer bounds (offset: %d, size: %d, total: %d)", ip, dataOffset, dataSize, totalSize)
	}

	// Copy fragment data into the correct position in the reassembly buffer
	copy(pm.ReceivedData[dataOffset:dataOffset+dataSize], fragmentData)
	for i := uint32(0); i < dataSize; i++ {
		if dataOffset+i < pm.TotalSize { // Boundary check
			setBit(pm.ReceivedMask, dataOffset+i) // Mark these bytes as received
		} else {
			log.Printf("Reassembler for %s: ReceivedMask out of bounds at offset %d, totalSize %d", ip, dataOffset+i, pm.TotalSize)
		}
	}

	// Check if the entire message is now complete
	isComplete := true
	for i := uint32(0); i < pm.TotalSize; i++ {
		if !getBit(pm.ReceivedMask, i) {
			isComplete = false
			break
		}
	}

	if isComplete {
		fullMessage := make([]byte, pm.TotalSize)
		copy(fullMessage, pm.ReceivedData) // Create a copy to return

		r.mu.Lock()
		r.currentMessage = nil // Remove the completed message from buffers
		r.mu.Unlock()

		log.Printf("Reassembler for %s: Message reassembled successfully (fn: %d, stage: %d, totalSize: %d)", ip, fn, stage, totalSize)
		return fullMessage, nil
	}

	return nil, nil // Not complete yet, or error in logic
}

// ==========================================
// 4. Device & Connection Logic
// ==========================================

// Device represents a probed device.
type Device struct {
	IP           string `json:"ip"`
	ID           string `json:"id"`
	Status       string `json:"status"`
	ConnectedVia string `json:"connected_via,omitempty"`
}

// DeviceConnection encapsulates a UDP connection to a device and its reassembly logic.
type DeviceConnection struct {
	Conn        *net.UDPConn
	StopChan    chan struct{}
	WG          sync.WaitGroup
	Reassembler *Reassembler // Each connection gets its own reassembler instance
}

// Send method encapsulates fragmentation and sending logic for a single logical message.
func (dc *DeviceConnection) Send(fn uint32, stage uint32, data []byte) error {
	packets := EncodeRequest(fn, stage, data)
	for i, packet := range packets {
		_, err := dc.Conn.Write(packet)
		if err != nil {
			return fmt.Errorf("failed to send fragment %d/%d: %v", i+1, len(packets), err)
		}
	}
	return nil
}

// ==========================================
// 5. Server State & Core Logic
// ==========================================

// ServerState holds the entire application state.
type ServerState struct {
	// Configuration paths
	LogDir     string
	FTPRootDir string
	StaticDir  string // For HTTP static files
	Timezone   string // For setting local time.Location

	Devices     map[string]*Device
	DevicesLock sync.RWMutex

	Connections     map[string]*DeviceConnection
	ConnectionsLock sync.Mutex

	// WebSocket Hubs
	LogClients    map[*websocket.Conn]bool
	LogLock       sync.Mutex
	DeviceClients map[*websocket.Conn]bool
	DeviceLock    sync.Mutex

	// Control Channels
	ScannerStopChan   chan struct{}
	LogServerStopChan chan struct{}

	// Timed Scan Logic
	ActiveScanResults map[string]bool
	ActiveScanLock    sync.Mutex

	// FTP Server Instance
	FTPServer *ftpserver.FtpServer

	// HTTP/WS Server instances for graceful shutdown
	httpServer *http.Server
	wsServer   *http.Server

	// Use a single mutex for the entire ServerState for simplicity in this global setup
	// although finer-grained locks are used for sub-components (DevicesLock, LogLock, etc.)
	mu sync.Mutex
}

// NewServerState creates a new instance of ServerState.
func NewServerState(logDir, ftpRootDir, staticDir, timezone string) *ServerState {
	return &ServerState{
		LogDir:            logDir,
		FTPRootDir:        ftpRootDir,
		StaticDir:         staticDir,
		Timezone:          timezone,
		Devices:           make(map[string]*Device),
		Connections:       make(map[string]*DeviceConnection),
		LogClients:        make(map[*websocket.Conn]bool),
		DeviceClients:     make(map[*websocket.Conn]bool),
		ActiveScanResults: make(map[string]bool),
	}
}

// StartHTTPAndWSServers starts the HTTP and WebSocket servers.
func (s *ServerState) StartHTTPAndWSServers() {
	// HTTP Server
	httpMux := http.NewServeMux()
	httpMux.Handle("/", http.FileServer(http.Dir(s.StaticDir))) // Serve static files from provided path
	httpMux.HandleFunc("/api/scanner/start", s.handleScannerStart)
	httpMux.HandleFunc("/api/scanner/stop", s.handleScannerStop)
	httpMux.HandleFunc("/api/scanner/refresh", s.handleScan)
	httpMux.HandleFunc("/api/scanner/status", s.handleScannerStatus)
	httpMux.HandleFunc("/api/log/start", s.handleLogStart)
	httpMux.HandleFunc("/api/log/stop", s.handleLogStop)
	httpMux.HandleFunc("/api/log/status", s.handleLogStatus)
	httpMux.HandleFunc("/api/connect", s.handleConnect)
	httpMux.HandleFunc("/api/disconnect", s.handleDisconnect)
	httpMux.HandleFunc("/api/send", s.handleSend)
	httpMux.HandleFunc("/api/batch_send", s.handleBatchSend)

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", HTTPPort),
		Handler: httpMux,
	}

	go func() {
		log.Printf("HTTP Server starting on :%d, Static Dir: %s", HTTPPort, s.StaticDir)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP Server failed to start: %v", err) // Use Printf instead of Fatal
		}
		log.Println("HTTP Server stopped.")
	}()

	// WebSocket Server
	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/", s.handleWS)

	s.wsServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", WSPort),
		Handler: wsMux,
	}

	go func() {
		log.Printf("WS Server starting on :%d", WSPort)
		if err := s.wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("WS Server failed to start: %v", err) // Use Printf instead of Fatal
		}
		log.Println("WS Server stopped.")
	}()

	// Give servers a moment to start
	time.Sleep(100 * time.Millisecond)
}

// StopHTTPAndWSServers stops the HTTP and WebSocket servers gracefully.
func (s *ServerState) StopHTTPAndWSServers() {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(ctx); err != nil {
			log.Printf("HTTP Server shutdown error: %v", err)
		}
		s.httpServer = nil // Clear reference
	}
	if s.wsServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.wsServer.Shutdown(ctx); err != nil {
			log.Printf("WS Server shutdown error: %v", err)
		}
		s.wsServer = nil // Clear reference
	}
}

// StartLogServer starts the UDP log server.
func (s *ServerState) StartLogServer() error {
	if s.LogServerStopChan != nil {
		return fmt.Errorf("log server already running")
	}

	if _, err := os.Stat(s.LogDir); os.IsNotExist(err) {
		if err := os.MkdirAll(s.LogDir, 0755); err != nil { // Use MkdirAll for recursive creation
			return fmt.Errorf("failed to create log directory %s: %v", s.LogDir, err)
		}
	}

	// Create a log file specific to the session or restart
	timestamp := time.Now().Format("20060102_150405")
	filename := filepath.Join(s.LogDir, fmt.Sprintf("session_log_%s.txt", timestamp))

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %v", filename, err)
	}
	log.Printf("Log file created: %s", filename)

	// Redirect Go's default logger to this file
	log.SetOutput(file)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	stopChan := make(chan struct{})
	s.LogServerStopChan = stopChan

	go func() {
		defer file.Close()
		addr := net.UDPAddr{Port: LogToolPort, IP: net.ParseIP("0.0.0.0")}
		conn, err := net.ListenUDP("udp", &addr)
		if err != nil {
			log.Printf("Log server bind error: %v", err)
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		for {
			select {
			case <-stopChan:
				log.Println("Log server stopped.")
				return
			default:
				conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				n, rAddr, err := conn.ReadFromUDP(buf) // rAddr here is *net.UDPAddr
				if err != nil {
					if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
						continue // Timeout is expected
					}
					log.Printf("Log server read error: %v", err)
					continue
				}
				msg := string(buf[:n])
				timeStr := time.Now().Format("2006-01-02 15:04:05.000")
				// rAddr 已经是 *net.UDPAddr 类型，可以直接访问 IP 字段
				formatted := fmt.Sprintf("[%s] [%s] %s", timeStr, rAddr.IP.String(), strings.TrimSpace(msg))
				s.BroadcastLog(formatted)          // Use s.BroadcastLog for WebSocket clients
				file.WriteString(formatted + "\n") // Write to file
			}
		}
	}()
	return nil
}

// StopLogServer stops the UDP log server.
func (s *ServerState) StopLogServer() error {
	if s.LogServerStopChan == nil {
		return fmt.Errorf("log server not running")
	}
	close(s.LogServerStopChan)
	s.LogServerStopChan = nil
	// Restore default logger output to stderr/stdout
	log.SetOutput(os.Stderr)
	return nil
}

// PushDevicesSnapshot sends the current device list to WebSocket clients.
func (s *ServerState) PushDevicesSnapshot() {
	s.DevicesLock.RLock()
	list := make([]*Device, 0, len(s.Devices))
	for _, d := range s.Devices {
		list = append(list, d)
	}
	s.DevicesLock.RUnlock()

	payload, _ := json.Marshal(map[string]interface{}{"type": "devices", "data": list})
	s.DeviceLock.Lock()
	for ws := range s.DeviceClients {
		if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
			log.Printf("Error writing to device WebSocket: %v", err)
			ws.Close()
			delete(s.DeviceClients, ws)
		}
	}
	s.DeviceLock.Unlock()
}

// BroadcastLog sends a log line to WebSocket clients.
func (s *ServerState) BroadcastLog(line string) {
	payload, _ := json.Marshal(map[string]string{"type": "log", "data": line})
	s.LogLock.Lock()
	for ws := range s.LogClients {
		if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
			log.Printf("Error writing to log WebSocket: %v", err)
			ws.Close()
			delete(s.LogClients, ws)
		}
	}
	s.LogLock.Unlock()
}

// UpdateDevice updates a device's status in the state.
func (s *ServerState) UpdateDevice(ip, id string) {
	s.ActiveScanLock.Lock()
	s.ActiveScanResults[ip] = true
	s.ActiveScanLock.Unlock()

	s.DevicesLock.Lock()
	defer s.DevicesLock.Unlock()

	displayID := id
	if displayID == "" {
		displayID = fmt.Sprintf("Unnamed_Device_%s", strings.ReplaceAll(ip, ".", "_"))
	}

	if dev, exists := s.Devices[ip]; !exists {
		s.Devices[ip] = &Device{IP: ip, ID: displayID, Status: "Available"}
		log.Printf("New Device: %s (%s)", ip, displayID)
	} else {
		if strings.HasPrefix(dev.ID, "Unnamed_Device_") && id != "" && dev.ID != displayID {
			dev.ID = displayID
		} else if dev.ID != displayID && id != "" {
			dev.ID = displayID
		}
	}
}

// RunUdpListener for broadcast scanning.
func (s *ServerState) RunUdpListener(stopChan chan struct{}) {
	addr := net.UDPAddr{Port: BroadcastPort, IP: net.ParseIP("0.0.0.0")}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		log.Printf("Scanner bind error: %v", err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 1024)
	for {
		select {
		case <-stopChan:
			log.Println("Scanner UDP listener stopped.")
			return
		default:
			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, remoteAddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
					continue // Timeout is expected
				}
				log.Printf("Scanner read error: %v", err)
				continue
			}

			// Validate IP address
			ip := remoteAddr.IP.String()
			if net.ParseIP(ip) == nil {
				log.Printf("Invalid IP received: %s", ip)
				continue
			}

			data := buf[:n]
			// Find null terminator and truncate if found
			if idx := bytes.IndexByte(data, 0); idx != -1 {
				data = data[:idx]
			}

			// Validate and sanitize the ID string
			id := strings.TrimSpace(string(data))
			if len(id) > 255 { // Limit ID length to prevent potential abuse
				id = id[:255]
			}

			// Sanitize ID to remove potentially harmful characters
			id = sanitizeDeviceID(id)

			s.UpdateDevice(ip, id)
			go s.PushDevicesSnapshot() // Push updates to clients
		}
	}
}

// sanitizeDeviceID removes potentially harmful characters from device IDs
func sanitizeDeviceID(id string) string {
	var sanitized strings.Builder
	for _, r := range id {
		// Only allow alphanumeric characters, spaces, hyphens, underscores
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
		   r == ' ' || r == '-' || r == '_' || r == '.' {
			sanitized.WriteRune(r)
		} else {
			// Replace other characters with underscore
			sanitized.WriteRune('_')
		}
	}
	return sanitized.String()
}

// StartContinuousScanner starts the continuous UDP broadcast scanner.
func (s *ServerState) StartContinuousScanner() error {
	if s.ScannerStopChan != nil {
		return fmt.Errorf("scanner already running")
	}
	stopChan := make(chan struct{})
	s.ScannerStopChan = stopChan
	go s.RunUdpListener(stopChan)
	log.Println("Continuous scanner started.")
	return nil
}

// StopContinuousScanner stops the continuous UDP broadcast scanner.
func (s *ServerState) StopContinuousScanner() error {
	if s.ScannerStopChan == nil {
		return fmt.Errorf("scanner not running")
	}
	close(s.ScannerStopChan)
	s.ScannerStopChan = nil
	log.Println("Continuous scanner stopped.")
	return nil
}

// PerformTimedScan performs a single 5-second scan.
func (s *ServerState) PerformTimedScan() {
	log.Println("Starting 5s timed scan...")

	s.ActiveScanLock.Lock()
	s.ActiveScanResults = make(map[string]bool)
	s.ActiveScanLock.Unlock()

	var tempStopChan chan struct{}

	if s.ScannerStopChan == nil { // Only start a temporary listener if continuous is not running
		tempStopChan = make(chan struct{})
		go s.RunUdpListener(tempStopChan)
	}

	time.Sleep(5 * time.Second)

	if tempStopChan != nil {
		close(tempStopChan) // Stop temporary listener
	}

	s.DevicesLock.Lock()
	s.ActiveScanLock.Lock()
	ipsToRemove := []string{}
	for ip, dev := range s.Devices {
		if dev.Status == "Available" && !s.ActiveScanResults[ip] {
			ipsToRemove = append(ipsToRemove, ip)
		}
	}
	for _, ip := range ipsToRemove {
		delete(s.Devices, ip)
		log.Printf("Removing stale device: %s", ip)
	}
	s.ActiveScanLock.Unlock()
	s.DevicesLock.Unlock()

	s.PushDevicesSnapshot()
	log.Println("Timed scan finished.")
}

// ConnectToDevice establishes a UDP connection to a specific device.
func (s *ServerState) ConnectToDevice(ip string) (string, error) {
	s.ConnectionsLock.Lock()
	defer s.ConnectionsLock.Unlock()
	if _, exists := s.Connections[ip]; exists {
		return "", nil // Already connected
	}

	raddr := &net.UDPAddr{IP: net.ParseIP(ip), Port: ProbeToolPort}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return "", err
	}

	localIP := conn.LocalAddr().(*net.UDPAddr).IP.String()
	stopChan := make(chan struct{})

	// For each connection, create and initialize a Reassembler instance
	reassembler := NewReassembler(30 * time.Second) // 30-second timeout for incomplete messages
	dc := &DeviceConnection{Conn: conn, StopChan: stopChan, Reassembler: reassembler}
	s.Connections[ip] = dc

	// Send periodic keep-alive/probe packets
	dc.WG.Add(1)
	go func() {
		defer dc.WG.Done()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				// Use dc.Send method to send, it handles fragmentation internally
				err := dc.Send(0xFFFFFFFF, 0, []byte{0x00})
				if err != nil {
					log.Printf("Error sending keep-alive to %s: %v", ip, err)
					// Decide if you want to disconnect on send error or just log
				}
			}
		}
	}()

	// Read responses and use the DeviceConnection's internal Reassembler
	dc.WG.Add(1)
	go func() {
		defer dc.WG.Done()
		buf := make([]byte, 4096) // Buffer size should be large enough to receive any single UDP packet
		for {
			select {
			case <-stopChan:
				return
			default:
				conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				n, rAddr, err := conn.ReadFrom(buf) // rAddr here is net.Addr, not *net.UDPAddr
				if err != nil {
					if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
						continue
					}
					log.Printf("Error reading from device %s: %v", ip, err)
					continue
				}

				// net.Conn.ReadFrom 返回的是 net.Addr 接口类型，需要断言为 *net.UDPAddr 才能访问 IP 字段
				remoteIPStr := ip // 默认使用传入的连接IP，以防类型断言失败
				if udpAddr, ok := rAddr.(*net.UDPAddr); ok && udpAddr != nil && udpAddr.IP != nil {
					remoteIPStr = udpAddr.IP.String()
				}

				// Decode the received packet/fragment
				info, totalSize, dataSize, dataOffset, fragmentData, decodeErr := DecodeRequest(buf[:n])
				if decodeErr != nil {
					s.BroadcastLog(fmt.Sprintf("Error decoding packet from %s: %v", remoteIPStr, decodeErr))
					continue
				}

				fn := info["fn"].(uint32)
				stage := info["stage"].(uint32)

				// Pass the fragment data to the connection's internal Reassembler.
				// The 'ip' parameter for AddFragment is mainly for logging context within Reassembler.
				fullData, reassemblyErr := dc.Reassembler.AddFragment(remoteIPStr, fn, stage, totalSize, dataSize, dataOffset, fragmentData)
				if reassemblyErr != nil {
					s.BroadcastLog(fmt.Sprintf("Reassembly error from %s: %v", remoteIPStr, reassemblyErr))
					continue
				}
				if fullData != nil {
					// Message is complete and reassembled, process it
					resp := fmt.Sprintf("CMD_RESP (Reassembled) from %s - FN:%d, STAGE:%d (TotalSize:%d)", remoteIPStr, fn, stage, totalSize)
					if len(fullData) > 0 {
						sData := string(fullData)
						if len(sData) > 50 {
							sData = sData[:50] + "..."
						}
						resp += fmt.Sprintf(", DATA: '%s'", sData)
					}
					s.BroadcastLog(resp)
				}
				// If fullData is nil, the message is not yet complete, so we do nothing.
				// The reassembler handles logging for timeouts.
			}
		}
	}()

	s.DevicesLock.Lock()
	if _, ok := s.Devices[ip]; !ok {
		s.Devices[ip] = &Device{IP: ip, ID: "Direct_Connect"}
	}
	s.Devices[ip].Status = "Connected"
	s.Devices[ip].ConnectedVia = localIP
	s.DevicesLock.Unlock()
	go s.PushDevicesSnapshot()
	return localIP, nil
}

// DisconnectFromDevice closes the connection to a device.
func (s *ServerState) DisconnectFromDevice(ip string) {
	s.ConnectionsLock.Lock()
	dc, exists := s.Connections[ip]
	if !exists {
		s.ConnectionsLock.Unlock()
		return
	}
	delete(s.Connections, ip)
	s.ConnectionsLock.Unlock()

	if dc != nil {
		close(dc.StopChan)
		dc.Conn.Close()
		dc.WG.Wait()
		// When connection is closed, clean up its Reassembler to avoid resource leaks
		if dc.Reassembler != nil {
			dc.Reassembler.Clear()       // Clear any potentially incomplete messages
			dc.Reassembler.StopCleanup() // Stop background cleanup goroutine
		}
	}

	s.DevicesLock.Lock()
	if dev, ok := s.Devices[ip]; ok {
		dev.Status = "Available"
		dev.ConnectedVia = ""
	}
	s.DevicesLock.Unlock()
	go s.PushDevicesSnapshot()
}

// ==========================================
// 6. FTP Server Implementation
// ==========================================

// FTPDriver implements ftpserverlib.MainDriver
type FTPDriver struct {
	BaseDir string
}

func (d *FTPDriver) GetSettings() (*ftpserver.Settings, error) {
	return &ftpserver.Settings{
		ListenAddr: fmt.Sprintf(":%d", FTPPort),
		PassiveTransferPortRange: &ftpserver.PortRange{
			Start: 50000,
			End:   50010,
		},
	}, nil
}

func (d *FTPDriver) ClientConnected(cc ftpserver.ClientContext) (string, error) {
	return "Welcome to Probe Tool FTP Server", nil
}

func (d *FTPDriver) ClientDisconnected(cc ftpserver.ClientContext) {}

func (d *FTPDriver) AuthUser(cc ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	// afero.NewOsFs() needs "github.com/spf13/afero"
	baseFs := afero.NewOsFs()
	if err := baseFs.MkdirAll(d.BaseDir, 0755); err != nil {
		return nil, err
	}
	restrictedFs := afero.NewBasePathFs(baseFs, d.BaseDir)

	if user == "user" && pass == "12345" {
		return &FTPClientDriver{Fs: restrictedFs}, nil
	}
	if user == "anonymous" {
		return &FTPClientDriver{Fs: restrictedFs}, nil
	}
	return nil, fmt.Errorf("login failed")
}

func (d *FTPDriver) GetTLSConfig() (*tls.Config, error) { return nil, nil }

// FTPClientDriver
type FTPClientDriver struct {
	afero.Fs
}

func (d *FTPClientDriver) GetSettings() (*ftpserver.Settings, error) { return nil, nil }

// StartFTPServer starts the FTP server.
func (s *ServerState) StartFTPServer() {
	if _, err := os.Stat(s.FTPRootDir); os.IsNotExist(err) {
		os.Mkdir(s.FTPRootDir, 0755)
	}

	driver := &FTPDriver{BaseDir: s.FTPRootDir}
	server := ftpserver.NewFtpServer(driver)

	go func() {
		log.Printf("FTP Server starting on :%d, Root: %s", FTPPort, s.FTPRootDir)
		if err := server.ListenAndServe(); err != nil {
			log.Printf("FTP Server error: %v", err)
		}
	}()
	s.FTPServer = server
}

// StopFTPServer stops the FTP server.
func (s *ServerState) StopFTPServer() {
	if s.FTPServer != nil {
		s.FTPServer.Stop()
		log.Println("FTP Server stopped.")
	}
}

// ==========================================
// 7. HTTP API & WS Handlers (Methods on ServerState)
// ==========================================

// sendJSON helper is not a method, it can remain a package-level function
func sendJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *ServerState) handleScannerStart(w http.ResponseWriter, r *http.Request) {
	if err := s.StartContinuousScanner(); err != nil {
		sendJSON(w, map[string]string{"status": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "scanner_started"})
	}
}
func (s *ServerState) handleScannerStop(w http.ResponseWriter, r *http.Request) {
	if err := s.StopContinuousScanner(); err != nil {
		sendJSON(w, map[string]string{"status": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "scanner_stopped"})
	}
}
func (s *ServerState) handleScan(w http.ResponseWriter, r *http.Request) {
	go s.PerformTimedScan()
	sendJSON(w, map[string]string{"status": "timed_scan_initiated"})
}
func (s *ServerState) handleScannerStatus(w http.ResponseWriter, r *http.Request) {
	st := "stopped"
	if s.ScannerStopChan != nil {
		st = "running"
	}
	sendJSON(w, map[string]string{"scanner_status": st})
}
func (s *ServerState) handleLogStart(w http.ResponseWriter, r *http.Request) {
	if err := s.StartLogServer(); err != nil {
		sendJSON(w, map[string]string{"status": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "started"})
	}
}
func (s *ServerState) handleLogStop(w http.ResponseWriter, r *http.Request) {
	if err := s.StopLogServer(); err != nil {
		sendJSON(w, map[string]string{"status": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "stopped"})
	}
}
func (s *ServerState) handleLogStatus(w http.ResponseWriter, r *http.Request) {
	st := "stopped"
	if s.LogServerStopChan != nil {
		st = "running"
	}
	sendJSON(w, map[string]string{"log_server_status": st})
}
func (s *ServerState) handleConnect(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IP string `json:"ip"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	if lIP, err := s.ConnectToDevice(b.IP); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		sendJSON(w, map[string]string{"status": "error", "reason": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "connected", "local_ip": lIP})
	}
}
func (s *ServerState) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IP string `json:"ip"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	s.DisconnectFromDevice(b.IP)
	sendJSON(w, map[string]string{"status": "disconnected"})
}

// handleSend now uses DeviceConnection's Send method, encapsulating fragmentation logic
func (s *ServerState) handleSend(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IP         string `json:"ip"`
		Fn         int    `json:"fn"`
		Stage      int    `json:"stage"`
		DataBase64 string `json:"data_base64"`
	}
	err := json.NewDecoder(r.Body).Decode(&b)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		sendJSON(w, map[string]string{"status": "error", "reason": "invalid request body"})
		return
	}

	raw, err := base64.StdEncoding.DecodeString(b.DataBase64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		sendJSON(w, map[string]string{"status": "error", "reason": "invalid base64 data"})
		return
	}

	if b.Stage == 0 { // Preserve client behavior
		raw = append(raw, 0)
	}

	s.ConnectionsLock.Lock()
	dc, ok := s.Connections[b.IP]
	s.ConnectionsLock.Unlock()
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		sendJSON(w, map[string]string{"status": "error", "reason": "not connected to device " + b.IP})
		return
	}

	if err := dc.Send(uint32(b.Fn), uint32(b.Stage), raw); err != nil {
		log.Printf("Error sending message to %s: %v", b.IP, err)
		w.WriteHeader(http.StatusInternalServerError)
		sendJSON(w, map[string]string{"status": "error", "reason": fmt.Sprintf("failed to send message: %v", err)})
		return
	}

	sendJSON(w, map[string]interface{}{"status": "sent", "fragments_sent_by_system": len(EncodeRequest(uint32(b.Fn), uint32(b.Stage), raw))})
}

// handleBatchSend handles sending multiple commands to one or more devices
func (s *ServerState) handleBatchSend(w http.ResponseWriter, r *http.Request) {
	var batchReq struct {
		Commands []struct {
			IP    string `json:"ip"`
			Text  string `json:"text"`
			Fn    int    `json:"fn"`
			Stage int    `json:"stage"`
		} `json:"commands"`
	}

	err := json.NewDecoder(r.Body).Decode(&batchReq)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		sendJSON(w, map[string]string{"status": "error", "reason": "invalid request body"})
		return
	}

	results := make([]map[string]interface{}, 0)

	for _, cmd := range batchReq.Commands {
		result := map[string]interface{}{"ip": cmd.IP, "text": cmd.Text}

		var finalFn, finalStage int
		var dataPayload []byte

		// If fn is -1, parse fn, stage, and data from the text field.
		if cmd.Fn == -1 {
			parts := strings.Fields(cmd.Text)
			if len(parts) == 0 {
				result["status"] = "error"
				result["reason"] = "empty data string for parsing"
				results = append(results, result)
				continue
			}

			// First part must be fn
			fn, err := strconv.Atoi(parts[0])
			if err != nil {
				result["status"] = "error"
				result["reason"] = "failed to parse fn from data string: " + parts[0]
				results = append(results, result)
				continue
			}
			finalFn = fn

			// Check for stage in the second part
			if len(parts) > 1 {
				stage, err := strconv.Atoi(parts[1])
				if err == nil { // It's a number, so it is a stage
					finalStage = stage
					if len(parts) > 2 {
						dataPayload = []byte(strings.Join(parts[2:], " "))
					}
				} else { // Not a number, so it is part of the data, and stage defaults to 0
					finalStage = 0
					dataPayload = []byte(strings.Join(parts[1:], " "))
				}
			} else {
				finalStage = 0
			}
		} else {
			finalFn = cmd.Fn
			finalStage = cmd.Stage
			dataPayload = []byte(cmd.Text)
		}

		raw := dataPayload
		if finalStage == 0 { // Preserve client behavior
			raw = append(raw, 0)
		}

		s.ConnectionsLock.Lock()
		dc, ok := s.Connections[cmd.IP]
		s.ConnectionsLock.Unlock()

		if !ok {
			result["status"] = "error"
			result["reason"] = "not connected to device"
			results = append(results, result)
			continue
		}

		if err := dc.Send(uint32(finalFn), uint32(finalStage), raw); err != nil {
			log.Printf("Error sending message to %s: %v", cmd.IP, err)
			result["status"] = "error"
			result["reason"] = fmt.Sprintf("failed to send message: %v", err)
		} else {
			result["status"] = "sent"
			result["fragments_sent_by_system"] = len(EncodeRequest(uint32(finalFn), uint32(finalStage), raw))
		}

		results = append(results, result)
	}

	sendJSON(w, map[string]interface{}{"results": results})
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func (s *ServerState) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer ws.Close()

	_, msg, err := ws.ReadMessage()
	if err != nil {
		log.Printf("WebSocket read message error: %v", err)
		return
	}
	var reg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg, &reg); err != nil {
		log.Printf("WebSocket JSON unmarshal error: %v", err)
		return
	}

	if reg.Type == "log" {
		s.LogLock.Lock()
		s.LogClients[ws] = true
		s.LogLock.Unlock()
		defer func() {
			s.LogLock.Lock()
			delete(s.LogClients, ws)
			s.LogLock.Unlock()
			log.Println("Log client disconnected")
		}()
		log.Println("Log client connected")
	} else if reg.Type == "devices" {
		s.DeviceLock.Lock()
		s.DeviceClients[ws] = true
		s.DeviceLock.Unlock()
		go s.PushDevicesSnapshot()
		defer func() {
			s.DeviceLock.Lock()
			delete(s.DeviceClients, ws)
			s.DeviceLock.Unlock()
			log.Println("Device client disconnected")
		}()
		log.Println("Device client connected")
	} else {
		log.Printf("Unknown WebSocket registration type: %s", reg.Type)
		return
	}

	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			break
		}
	}
}
