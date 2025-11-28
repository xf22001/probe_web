package probetool

import (
	"bytes"
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
	"strings"
	"sync"
	"time"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/gorilla/websocket"
	"github.com/spf13/afero"
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
)

var (
	LogDir     = "logs"
	FTPRootDir = "ftp_share"
	httpServer *http.Server
)

// ==========================================
// 2. Protocol Logic (CRC & Packet)
// ==========================================

const (
	DefaultRequestMagic = 0xA5A55A5A
	HeaderSize          = 17
	PayloadInfoSize     = 8
	RequestTSize        = HeaderSize + PayloadInfoSize
)

func CalcCRC8(data []byte) uint8 {
	var crc uint8 = 0
	for _, b := range data {
		crc = (crc + b) & 0xFF
	}
	return crc
}

func EncodeRequest(fn uint32, stage uint32, data []byte) []byte {
	payloadBuf := new(bytes.Buffer)
	binary.Write(payloadBuf, binary.LittleEndian, fn)
	binary.Write(payloadBuf, binary.LittleEndian, stage)
	payloadBytes := payloadBuf.Bytes()

	crcData := append(payloadBytes, data...)
	crc := CalcCRC8(crcData)

	headerBuf := new(bytes.Buffer)
	binary.Write(headerBuf, binary.LittleEndian, uint32(DefaultRequestMagic))
	binary.Write(headerBuf, binary.LittleEndian, uint32(len(data)))
	binary.Write(headerBuf, binary.LittleEndian, uint32(len(data)))
	binary.Write(headerBuf, binary.LittleEndian, uint32(0))
	binary.Write(headerBuf, binary.LittleEndian, crc)

	return append(append(headerBuf.Bytes(), payloadBytes...), data...)
}

func DecodeRequest(buffer []byte) (map[string]interface{}, []byte, error) {
	if len(buffer) < RequestTSize {
		return nil, nil, fmt.Errorf("incomplete buffer")
	}
	headerBytes := buffer[:HeaderSize]
	payloadInfoBytes := buffer[HeaderSize:RequestTSize]

	var magic, dataSize uint32
	var crcReceived uint8
	reader := bytes.NewReader(headerBytes)
	binary.Read(reader, binary.LittleEndian, &magic)
	reader.Seek(4, 1) // skip total_size
	binary.Read(reader, binary.LittleEndian, &dataSize)
	reader.Seek(4, 1) // skip data_offset
	binary.Read(reader, binary.LittleEndian, &crcReceived)

	if magic != DefaultRequestMagic {
		return nil, nil, fmt.Errorf("invalid magic")
	}
	if len(buffer) < int(RequestTSize+dataSize) {
		return nil, nil, fmt.Errorf("incomplete data payload")
	}

	actualDataBytes := buffer[RequestTSize : RequestTSize+int(dataSize)]
	crcData := append(payloadInfoBytes, actualDataBytes...)
	if crcReceived != CalcCRC8(crcData) {
		return nil, nil, fmt.Errorf("invalid crc")
	}

	var fn, stage uint32
	payloadReader := bytes.NewReader(payloadInfoBytes)
	binary.Read(payloadReader, binary.LittleEndian, &fn)
	binary.Read(payloadReader, binary.LittleEndian, &stage)

	return map[string]interface{}{"fn": fn, "stage": stage}, actualDataBytes, nil
}

// ==========================================
// 3. Global State
// ==========================================

type Device struct {
	IP           string `json:"ip"`
	ID           string `json:"id"`
	Status       string `json:"status"`
	ConnectedVia string `json:"connected_via,omitempty"`
}

type DeviceConnection struct {
	Conn     *net.UDPConn
	StopChan chan struct{}
	WG       sync.WaitGroup
}

type ServerState struct {
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
}

var state = &ServerState{
	Devices:           make(map[string]*Device),
	Connections:       make(map[string]*DeviceConnection),
	LogClients:        make(map[*websocket.Conn]bool),
	DeviceClients:     make(map[*websocket.Conn]bool),
	ActiveScanResults: make(map[string]bool),
}

// ==========================================
// 4. FTP Server Implementation (Final Fix)
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
	// 创建基础文件系统
	baseFs := afero.NewOsFs()
	if err := baseFs.MkdirAll(d.BaseDir, 0755); err != nil {
		return nil, err
	}
	// 限制在 BaseDir 下
	restrictedFs := afero.NewBasePathFs(baseFs, d.BaseDir)

	if user == "user" && pass == "123" {
		// 直接使用嵌入了 afero.Fs 的结构体，自动获得所有文件操作方法
		return &FTPClientDriver{Fs: restrictedFs}, nil
	}
	if user == "anonymous" {
		return &FTPClientDriver{Fs: restrictedFs}, nil
	}
	return nil, fmt.Errorf("login failed")
}

func (d *FTPDriver) GetTLSConfig() (*tls.Config, error) { return nil, nil }

// FTPClientDriver 关键修复：直接嵌入 afero.Fs 接口
// 这样它就自动实现了 ClientDriver 所需的所有文件操作方法 (Create, Mkdir, Remove 等)
// 无需手动一个个去写。
type FTPClientDriver struct {
	afero.Fs
}

// GetSettings 是 ClientDriver 接口可能需要的唯一额外方法（取决于具体版本），提供一个默认实现
func (d *FTPClientDriver) GetSettings() (*ftpserver.Settings, error) { return nil, nil }

func StartFTPServer() {
	if _, err := os.Stat(FTPRootDir); os.IsNotExist(err) {
		os.Mkdir(FTPRootDir, 0755)
	}

	driver := &FTPDriver{BaseDir: FTPRootDir}
	server := ftpserver.NewFtpServer(driver)

	go func() {
		log.Printf("FTP Server starting on :%d, Root: %s", FTPPort, FTPRootDir)
		if err := server.ListenAndServe(); err != nil {
			log.Printf("FTP Server error: %v", err)
		}
	}()
	state.FTPServer = server
}

// ==========================================
// 5. Business Logic (Scanner, Log, Connect)
// ==========================================

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
			ws.Close()
			delete(s.DeviceClients, ws)
		}
	}
	s.DeviceLock.Unlock()
}

func (s *ServerState) BroadcastLog(line string) {
	payload, _ := json.Marshal(map[string]string{"type": "log", "data": line})
	s.LogLock.Lock()
	for ws := range s.LogClients {
		if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
			ws.Close()
			delete(s.LogClients, ws)
		}
	}
	s.LogLock.Unlock()
}

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

// --- Scanner Logic ---

func RunUdpListener(stopChan chan struct{}) {
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
			return
		default:
			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, remoteAddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			ip := remoteAddr.IP.String()
			data := buf[:n]
			if idx := bytes.IndexByte(data, 0); idx != -1 {
				data = data[:idx]
			}
			id := strings.TrimSpace(string(data))

			state.UpdateDevice(ip, id)
			go state.PushDevicesSnapshot()
		}
	}
}

func StartContinuousScanner() error {
	if state.ScannerStopChan != nil {
		return fmt.Errorf("scanner_already_running")
	}
	stopChan := make(chan struct{})
	state.ScannerStopChan = stopChan
	go RunUdpListener(stopChan)
	return nil
}

func StopContinuousScanner() error {
	if state.ScannerStopChan == nil {
		return fmt.Errorf("scanner_not_running")
	}
	close(state.ScannerStopChan)
	state.ScannerStopChan = nil
	return nil
}

func PerformTimedScan() {
	log.Println("Starting 5s timed scan...")

	state.ActiveScanLock.Lock()
	state.ActiveScanResults = make(map[string]bool)
	state.ActiveScanLock.Unlock()

	var tempStopChan chan struct{}

	if state.ScannerStopChan == nil {
		tempStopChan = make(chan struct{})
		go RunUdpListener(tempStopChan)
	}

	time.Sleep(5 * time.Second)

	if tempStopChan != nil {
		close(tempStopChan)
	}

	state.DevicesLock.Lock()
	state.ActiveScanLock.Lock()
	ipsToRemove := []string{}
	for ip, dev := range state.Devices {
		if dev.Status == "Available" && !state.ActiveScanResults[ip] {
			ipsToRemove = append(ipsToRemove, ip)
		}
	}
	for _, ip := range ipsToRemove {
		delete(state.Devices, ip)
		log.Printf("Removing stale device: %s", ip)
	}
	state.ActiveScanLock.Unlock()
	state.DevicesLock.Unlock()

	state.PushDevicesSnapshot()
	log.Println("Timed scan finished.")
}

// --- Log Server Logic ---

func StartLogServer() error {
	if state.LogServerStopChan != nil {
		return fmt.Errorf("already_running")
	}
	if _, err := os.Stat(LogDir); os.IsNotExist(err) {
		os.Mkdir(LogDir, 0755)
	}
	timestamp := time.Now().Format("20060102_150405")
	filename := filepath.Join(LogDir, fmt.Sprintf("log_%s.txt", timestamp))

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	log.Printf("Log file created: %s", filename)

	stopChan := make(chan struct{})
	state.LogServerStopChan = stopChan

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
				return
			default:
				conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				n, rAddr, err := conn.ReadFromUDP(buf)
				if err != nil {
					continue
				}
				msg := string(buf[:n])
				timeStr := time.Now().Format("2006-01-02 15:04:05.000")
				formatted := fmt.Sprintf("[%s] [%s] %s", timeStr, rAddr.IP.String(), strings.TrimSpace(msg))
				state.BroadcastLog(formatted)
				file.WriteString(formatted + "\n")
			}
		}
	}()
	return nil
}

func StopLogServer() error {
	if state.LogServerStopChan == nil {
		return fmt.Errorf("not_running")
	}
	close(state.LogServerStopChan)
	state.LogServerStopChan = nil
	return nil
}

// --- Connector Logic ---

func ConnectToDevice(ip string) (string, error) {
	state.ConnectionsLock.Lock()
	defer state.ConnectionsLock.Unlock()
	if _, exists := state.Connections[ip]; exists {
		return "", nil
	}

	raddr := &net.UDPAddr{IP: net.ParseIP(ip), Port: ProbeToolPort}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return "", err
	}

	localIP := strings.Split(conn.LocalAddr().String(), ":")[0]
	stopChan := make(chan struct{})
	dc := &DeviceConnection{Conn: conn, StopChan: stopChan}
	state.Connections[ip] = dc

	dc.WG.Add(1)
	go func() {
		defer dc.WG.Done()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		payload := EncodeRequest(0xFFFFFFFF, 0, []byte{0x00})
		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				conn.Write(payload)
			}
		}
	}()

	dc.WG.Add(1)
	go func() {
		defer dc.WG.Done()
		buf := make([]byte, 4096)
		for {
			select {
			case <-stopChan:
				return
			default:
				conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				n, _, err := conn.ReadFrom(buf)
				if err != nil {
					continue
				}
				info, data, err := DecodeRequest(buf[:n])
				if err == nil {
					resp := fmt.Sprintf("CMD_RESP from %s - FN:%d, STAGE:%d", ip, info["fn"], info["stage"])
					if len(data) > 0 {
						sData := string(data)
						if len(sData) > 50 {
							sData = sData[:50] + "..."
						}
						resp += fmt.Sprintf(", DATA: '%s'", sData)
					}
					state.BroadcastLog(resp)
				}
			}
		}
	}()

	state.DevicesLock.Lock()
	if _, ok := state.Devices[ip]; !ok {
		state.Devices[ip] = &Device{IP: ip, ID: "Direct_Connect"}
	}
	state.Devices[ip].Status = "Connected"
	state.Devices[ip].ConnectedVia = localIP
	state.DevicesLock.Unlock()
	go state.PushDevicesSnapshot()
	return localIP, nil
}

func DisconnectFromDevice(ip string) {
	state.ConnectionsLock.Lock()
	dc, exists := state.Connections[ip]
	if !exists {
		state.ConnectionsLock.Unlock()
		return
	}
	delete(state.Connections, ip)
	state.ConnectionsLock.Unlock()

	close(dc.StopChan)
	dc.Conn.Close()
	dc.WG.Wait()

	state.DevicesLock.Lock()
	if dev, ok := state.Devices[ip]; ok {
		dev.Status = "Available"
		dev.ConnectedVia = ""
	}
	state.DevicesLock.Unlock()
	go state.PushDevicesSnapshot()
}

// ==========================================
// 6. HTTP API & WS Handlers
// ==========================================

func sendJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func handleScannerStart(w http.ResponseWriter, r *http.Request) {
	if err := StartContinuousScanner(); err != nil {
		sendJSON(w, map[string]string{"status": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "scanner_started"})
	}
}
func handleScannerStop(w http.ResponseWriter, r *http.Request) {
	if err := StopContinuousScanner(); err != nil {
		sendJSON(w, map[string]string{"status": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "scanner_stopped"})
	}
}
func handleScan(w http.ResponseWriter, r *http.Request) {
	go PerformTimedScan()
	sendJSON(w, map[string]string{"status": "timed_scan_initiated"})
}
func handleScannerStatus(w http.ResponseWriter, r *http.Request) {
	st := "stopped"
	if state.ScannerStopChan != nil {
		st = "running"
	}
	sendJSON(w, map[string]string{"scanner_status": st})
}
func handleLogStart(w http.ResponseWriter, r *http.Request) {
	if err := StartLogServer(); err != nil {
		sendJSON(w, map[string]string{"status": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "started"})
	}
}
func handleLogStop(w http.ResponseWriter, r *http.Request) {
	if err := StopLogServer(); err != nil {
		sendJSON(w, map[string]string{"status": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "stopped"})
	}
}
func handleLogStatus(w http.ResponseWriter, r *http.Request) {
	st := "stopped"
	if state.LogServerStopChan != nil {
		st = "running"
	}
	sendJSON(w, map[string]string{"log_server_status": st})
}
func handleConnect(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IP string `json:"ip"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	if lIP, err := ConnectToDevice(b.IP); err != nil {
		w.WriteHeader(500)
		sendJSON(w, map[string]string{"status": "error", "reason": err.Error()})
	} else {
		sendJSON(w, map[string]string{"status": "connected", "local_ip": lIP})
	}
}
func handleDisconnect(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IP string `json:"ip"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	DisconnectFromDevice(b.IP)
	sendJSON(w, map[string]string{"status": "disconnected"})
}
func handleSend(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IP         string `json:"ip"`
		Fn         int    `json:"fn"`
		Stage      int    `json:"stage"`
		DataBase64 string `json:"data_base64"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	raw, _ := base64.StdEncoding.DecodeString(b.DataBase64)
	payload := EncodeRequest(uint32(b.Fn), uint32(b.Stage), raw)
	state.ConnectionsLock.Lock()
	dc, ok := state.Connections[b.IP]
	state.ConnectionsLock.Unlock()
	if !ok {
		w.WriteHeader(400)
		sendJSON(w, map[string]string{"status": "error"})
		return
	}
	dc.Conn.Write(payload)
	sendJSON(w, map[string]string{"status": "sent"})
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	_, msg, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		return
	}
	var reg struct {
		Type string `json:"type"`
	}
	json.Unmarshal(msg, &reg)
	if reg.Type == "log" {
		state.LogLock.Lock()
		state.LogClients[ws] = true
		state.LogLock.Unlock()
		defer func() { state.LogLock.Lock(); delete(state.LogClients, ws); state.LogLock.Unlock() }()
	} else if reg.Type == "devices" {
		state.DeviceLock.Lock()
		state.DeviceClients[ws] = true
		state.DeviceLock.Unlock()
		go state.PushDevicesSnapshot()
		defer func() { state.DeviceLock.Lock(); delete(state.DeviceClients, ws); state.DeviceLock.Unlock() }()
	}
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			break
		}
	}
	ws.Close()
}

// ==========================================
// 7. Main Entry
// ==========================================

//export Start
func Start(ftpDir, logDir, staticDir string) {
	FTPRootDir = ftpDir
	LogDir = logDir

	StartLogServer()
	StartFTPServer()
	go PerformTimedScan()

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(staticDir)))
	mux.HandleFunc("/api/scanner/start", handleScannerStart)
	mux.HandleFunc("/api/scanner/stop", handleScannerStop)
	mux.HandleFunc("/api/scanner_status", handleScannerStatus)
	mux.HandleFunc("/api/scan", handleScan)
	mux.HandleFunc("/api/log/start", handleLogStart)
	mux.HandleFunc("/api/log/stop", handleLogStop)
	mux.HandleFunc("/api/log_server_status", handleLogStatus)
	mux.HandleFunc("/api/connect", handleConnect)
	mux.HandleFunc("/api/disconnect", handleDisconnect)
	mux.HandleFunc("/api/send", handleSend)

	go func() {
		log.Printf("WS Server on :%d", WSPort)
		wsMux := http.NewServeMux()
		wsMux.HandleFunc("/", handleWS)
		http.ListenAndServe(fmt.Sprintf(":%d", WSPort), wsMux)
	}()

	httpServer = &http.Server{Addr: fmt.Sprintf(":%d", HTTPPort), Handler: mux}
	log.Printf("HTTP Server on :%d", HTTPPort)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

//export Stop
func Stop() {
	if state.FTPServer != nil {
		state.FTPServer.Stop()
	}
	if httpServer != nil {
		httpServer.Close()
	}
	StopLogServer()
	StopContinuousScanner()
}

func main() {}
