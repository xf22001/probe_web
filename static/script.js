// ./static/script.js

document.addEventListener('DOMContentLoaded', () => {
    // === 获取所有前端元素 ===
    // Device Discovery Section
    const startScannerButton = document.getElementById('startScannerButton');
    const stopScannerButton = document.getElementById('stopScannerButton');
    const scanButton = document.getElementById('scanButton'); 
    const deviceList = document.getElementById('deviceList');

    // Device Control Section
    const deviceSelect = document.getElementById('deviceSelect');
    const connectButton = document.getElementById('connectButton');
    const disconnectButton = document.getElementById('disconnectButton');
    const connectionInfo = document.getElementById('connectionInfo');
    
    // Send Command (Text) Section
    const commandInput = document.getElementById('commandInput');
    const sendCommandButton = document.getElementById('sendCommandButton');
    const commandHistoryList = document.getElementById('commandHistoryList');
    
    // Log Stream Section
    const startLogButton = document.getElementById('startLogButton');
    const stopLogButton = document.getElementById('stopLogButton');
    const clearLogButton = document.getElementById('clearLogButton'); 
    const logOutput = document.getElementById('logOutput');

    // 新增：侧边栏相关元素
    const toggleSidePanelButton = document.getElementById('toggleSidePanelButton');
    const sidePanel = document.querySelector('.side-panel');

    // === 内部状态变量 ===
    let currentDevices = []; 
    let scannerIsRunning = false; 
    let logServerIsRunning = false; 
    // 新增：侧边栏状态
    let isSidePanelCollapsed = false; 
    const SIDE_PANEL_STATE_KEY = 'probe_tool_side_panel_collapsed'; 

    // === WebSocket 变量 (修改为 let 以便重连时重新赋值) ===
    let wsDevices = null;
    let wsLog = null;
    const RECONNECT_DELAY = 3000; // 断开后3秒重连

    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${wsProtocol}//${window.location.hostname}:8001`;

    // === WebSocket 连接函数 ===
    
    function connectDeviceWebSocket() {
        console.log('JS DEBUG: Attempting to connect Device WebSocket...');
        wsDevices = new WebSocket(wsUrl);

        wsDevices.onopen = () => {
            console.log('JS DEBUG: Connected to Device WebSocket. Sending registration...');
            wsDevices.send(JSON.stringify({ type: 'devices' })); 
            fetchScannerStatus(); 
            fetchLogServerStatus(); 
        };

        wsDevices.onmessage = (event) => {
            try {
                const message = JSON.parse(event.data);
                if (message.type === 'devices') {
                    currentDevices = message.data;
                    updateDeviceList(currentDevices); 
                    updateDeviceSelect(currentDevices);
                } else if (message.type === 'info') { 
                    console.info('JS INFO: Device WS Info:', message.data);
                }
            } catch (e) {
                console.error('JS ERROR: Error parsing device WS message:', e, event.data);
            }
        };

        wsDevices.onclose = (event) => {
            console.log(`JS DEBUG: Device WebSocket closed (Code: ${event.code}). Reconnecting in ${RECONNECT_DELAY}ms...`);
            setTimeout(connectDeviceWebSocket, RECONNECT_DELAY);
        };

        wsDevices.onerror = (error) => {
            console.error('JS ERROR: Device WebSocket error:', error);
        };
    }

    function connectLogWebSocket() {
        console.log('JS DEBUG: Attempting to connect Log WebSocket...');
        wsLog = new WebSocket(wsUrl);

        wsLog.onopen = () => {
            console.log('JS DEBUG: Connected to Log WebSocket');
            wsLog.send(JSON.stringify({ type: 'log' })); 
        };

        wsLog.onmessage = (event) => {
            try {
                const message = JSON.parse(event.data);
                if (message.type === 'log') {
                    const logLine = document.createElement('div');
                    if (message.data.startsWith('CMD_RESP from')) {
                        logLine.className = 'command-response'; 
                    }
                    logLine.textContent = message.data;
                    
                    const scrollTolerance = 5; 
                    const isScrolledToBottom = (logOutput.scrollHeight - logOutput.clientHeight) <= (logOutput.scrollTop + scrollTolerance);

                    logOutput.appendChild(logLine); 

                    if (isScrolledToBottom) {
                        logOutput.scrollTop = logOutput.scrollHeight;
                    }
                }
            } catch (e) {
                // 忽略非JSON日志或解析错误
            }
        };

        wsLog.onclose = (event) => {
            console.log(`JS DEBUG: Log WebSocket disconnected. Reconnecting in ${RECONNECT_DELAY}ms...`);
            setTimeout(connectLogWebSocket, RECONNECT_DELAY);
        };

        wsLog.onerror = (error) => {
            console.error('JS ERROR: Log WebSocket error:', error);
        };
    }

    // === 启动 WebSocket 连接 ===
    connectDeviceWebSocket();
    connectLogWebSocket();

    // === 历史记录管理逻辑 ===
    const MAX_HISTORY_ITEMS = 10;
    const HISTORY_STORAGE_KEY = 'probe_tool_cmd_history';
    let commandHistory = [];

    function loadHistory() {
        try {
            const stored = localStorage.getItem(HISTORY_STORAGE_KEY);
            if (stored) {
                commandHistory = JSON.parse(stored);
            }
        } catch (e) {
            console.error('JS ERROR: Failed to load history:', e);
            commandHistory = [];
        }
    }

    function saveHistory() {
        localStorage.setItem(HISTORY_STORAGE_KEY, JSON.stringify(commandHistory));
    }

    function addCommandToHistory(cmd) {
        if (!cmd) return;
        const existingIndex = commandHistory.indexOf(cmd);
        if (existingIndex !== -1) {
            commandHistory.splice(existingIndex, 1);
        }
        commandHistory.unshift(cmd);
        if (commandHistory.length > MAX_HISTORY_ITEMS) {
            commandHistory.pop();
        }
        saveHistory();
        renderHistoryList();
    }

    function deleteHistoryItem(e, index) {
        e.stopPropagation(); 
        commandHistory.splice(index, 1);
        saveHistory();
        renderHistoryList();
        if (commandHistory.length === 0) {
            commandHistoryList.style.display = 'none';
        }
    }

    function renderHistoryList() {
        commandHistoryList.innerHTML = '';
        if (commandHistory.length === 0) {
            commandHistoryList.style.display = 'none';
            return;
        }

        commandHistory.forEach((cmd, index) => {
            const li = document.createElement('li');
            
            const spanText = document.createElement('span');
            spanText.className = 'history-text';
            spanText.textContent = cmd;
            
            li.addEventListener('click', () => {
                commandInput.value = cmd;
                commandHistoryList.style.display = 'none'; 
            });

            const spanDelete = document.createElement('span');
            spanDelete.className = 'history-delete-btn';
            spanDelete.innerHTML = '&times;'; 
            spanDelete.title = 'Remove from history';
            spanDelete.addEventListener('click', (e) => deleteHistoryItem(e, index));

            li.appendChild(spanText);
            li.appendChild(spanDelete);
            commandHistoryList.appendChild(li);
        });
    }

    loadHistory();

    commandInput.addEventListener('focus', () => {
        if (commandHistory.length > 0) {
            renderHistoryList();
            commandHistoryList.style.display = 'block';
        }
    });

    document.addEventListener('click', (e) => {
        if (!commandInput.contains(e.target) && !commandHistoryList.contains(e.target)) {
            commandHistoryList.style.display = 'none';
        }
    });

    // === UI 更新函数 ===

    function updateDeviceControlButtons() {
        const selectedIp = deviceSelect.value;
        const selectedDevice = currentDevices.find(d => d.ip === selectedIp);
        const isConnected = selectedDevice && selectedDevice.status === 'Connected';

        connectButton.disabled = !selectedIp || isConnected;
        disconnectButton.disabled = !selectedIp || !isConnected;
        sendCommandButton.disabled = !isConnected; 

        if (isConnected && selectedDevice.connected_via) {
            connectionInfo.textContent = `Via Local IP: ${selectedDevice.connected_via}`;
            connectionInfo.style.color = '#006400'; 
        } else if (selectedDevice && selectedDevice.status === 'Available') {
            connectionInfo.textContent = 'Device Available';
            connectionInfo.style.color = '#666';
        } else {
            connectionInfo.textContent = ''; 
        }
    }

    function updateScannerButtons() {
        startScannerButton.disabled = scannerIsRunning;
        stopScannerButton.disabled = !scannerIsRunning;
        scanButton.disabled = scannerIsRunning; 
    }

    function updateLogServerButtons() {
        startLogButton.disabled = logServerIsRunning;
        stopLogButton.disabled = !logServerIsRunning;
    }

    function updateDeviceList(devices) {
        deviceList.innerHTML = ''; 
        if (!devices || devices.length === 0) {
            deviceList.innerHTML = '<li>No devices found. Start scanner or click "Refresh Devices"</li>'; 
            return;
        }
        devices.forEach(device => {
            if (device.ip && device.id !== undefined && device.id !== null) {
                const li = document.createElement('li');
                li.textContent = `${device.ip}-[${device.id}] Status: ${device.status}`;
                deviceList.appendChild(li);
            }
        });
    }

    function updateDeviceSelect(devices) {
        const selectedIp = deviceSelect.value; 
        deviceSelect.innerHTML = '<option value="">-- Select a device --</option>'; 
        if (!devices || devices.length === 0) {
            updateDeviceControlButtons(); 
            return;
        }
        devices.forEach(device => {
            if (device.ip && device.id !== undefined && device.id !== null) {
                const option = document.createElement('option');
                option.value = device.ip;
                option.textContent = `${device.id} (${device.ip}) - ${device.status}`;
                if (device.ip === selectedIp) { 
                    option.selected = true;
                }
                deviceSelect.appendChild(option);
            }
        });
        updateDeviceControlButtons(); 
    }

    async function fetchScannerStatus() {
        try {
            const response = await fetch('/api/scanner_status');
            const data = await response.json();
            scannerIsRunning = (data.scanner_status === 'running');
            updateScannerButtons();
        } catch (error) {
            console.error('JS ERROR: Error fetching scanner status:', error);
            scannerIsRunning = false; 
            updateScannerButtons();
        }
    }

    async function fetchLogServerStatus() {
        try {
            const response = await fetch('/api/log_server_status');
            const data = await response.json();
            logServerIsRunning = (data.log_server_status === 'running');
            updateLogServerButtons();
        } catch (error) {
            console.error('JS ERROR: Error fetching log server status:', error);
            logServerIsRunning = false; 
            updateLogServerButtons();
        }
    }

    // 新增：侧边栏状态管理函数
    function applySidePanelState() {
        if (isSidePanelCollapsed) {
            sidePanel.classList.add('collapsed');
            toggleSidePanelButton.textContent = '展开面板';
            toggleSidePanelButton.setAttribute('aria-expanded', 'false');
        } else {
            sidePanel.classList.remove('collapsed');
            toggleSidePanelButton.textContent = '收起面板';
            toggleSidePanelButton.setAttribute('aria-expanded', 'true');
        }
    }

    function loadSidePanelState() {
        try {
            const storedState = localStorage.getItem(SIDE_PANEL_STATE_KEY);
            if (storedState !== null) {
                isSidePanelCollapsed = JSON.parse(storedState);
            }
        } catch (e) {
            console.error('JS ERROR: Failed to load side panel state from localStorage:', e);
            isSidePanelCollapsed = false; // 出错时默认展开
        }
        applySidePanelState();
    }

    function saveSidePanelState() {
        try {
            localStorage.setItem(SIDE_PANEL_STATE_KEY, JSON.stringify(isSidePanelCollapsed));
        } catch (e) {
            console.error('JS ERROR: Failed to save side panel state to localStorage:', e);
        }
    }

    // === Event Listeners ===

    startScannerButton.addEventListener('click', async () => {
        startScannerButton.disabled = true; 
        deviceList.innerHTML = '<li>Scanning for devices (continuous)...</li>'; 
        deviceSelect.innerHTML = '<option value="">-- Select a device --</option>'; 
        currentDevices = []; 

        try {
            const response = await fetch('/api/scanner/start', { method: 'POST' });
            const data = await response.json();
            if (data.status === 'scanner_started' || data.status === 'scanner_already_running') {
                scannerIsRunning = true;
            }
        } catch (error) {
            console.error('JS ERROR: Error starting continuous scanner:', error);
            deviceList.innerHTML = '<li>Error starting scanner.</li>'; 
        } finally {
            updateScannerButtons(); 
        }
    });

    stopScannerButton.addEventListener('click', async () => {
        stopScannerButton.disabled = true; 
        try {
            const response = await fetch('/api/scanner/stop', { method: 'POST' });
            const data = await response.json();
            if (data.status === 'scanner_stopped' || data.status === 'scanner_not_running') {
                scannerIsRunning = false;
            }
        } catch (error) {
            console.error('JS ERROR: Error stopping continuous scanner:', error);
        } finally {
            updateScannerButtons(); 
        }
    });

    scanButton.addEventListener('click', async () => {
        scanButton.disabled = true; 
        deviceList.innerHTML = '<li>Scanning for devices (5 seconds)...</li>'; 
        deviceSelect.innerHTML = '<option value="">-- Select a device --</option>'; 
        currentDevices = []; 

        try {
            const response = await fetch('/api/scan', { method: 'POST' });
        } catch (error) {
            console.error('JS ERROR: Error initiating device list refresh:', error);
            deviceList.innerHTML = '<li>Error initiating scan.</li>'; 
        } finally {
            scanButton.disabled = scannerIsRunning; 
        }
    });

    deviceSelect.addEventListener('change', updateDeviceControlButtons);

    connectButton.addEventListener('click', async () => {
        const ip = deviceSelect.value;
        if (!ip) return; 
        connectButton.disabled = true; 
        try {
            await fetch('/api/connect', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ip })
            });
        } catch (error) {
            console.error(`JS ERROR: Error connecting to ${ip}:`, error);
        }
    });

    disconnectButton.addEventListener('click', async () => {
        const ip = deviceSelect.value;
        if (!ip) return; 
        disconnectButton.disabled = true; 
        try {
            await fetch('/api/disconnect', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ip })
            });
        } catch (error) {
            console.error(`JS ERROR: Error disconnecting from ${ip}:`, error);
        }
    });

    sendCommandButton.addEventListener('click', async () => {
        const ip = deviceSelect.value;
        const text = commandInput.value;
        if (!ip || !text) return; 

        addCommandToHistory(text);

        sendCommandButton.disabled = true; 
        
        let fnToSend = 0; 
        const stageToSend = 0; 
        const dataToSendRaw = text; 

        const match = text.match(/^(\d+)/); 
        if (match && match[1]) {
            fnToSend = parseInt(match[1], 10);
        }

        const dataToSendBase64 = btoa(unescape(encodeURIComponent(dataToSendRaw)));

        try {
            await fetch('/api/send', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ip, fn: fnToSend, stage: stageToSend, data_base64: dataToSendBase64 })
            });
        } catch (error) {
            console.error(`JS ERROR: Error sending command:`, error);
        } finally {
            sendCommandButton.disabled = false; 
        }
    });

    commandInput.addEventListener('keydown', (event) => {
        if (event.key === 'Enter') {
            event.preventDefault(); 
            if (!sendCommandButton.disabled) {
                sendCommandButton.click();
            }
        }
    });

    startLogButton.addEventListener('click', async () => {
        startLogButton.disabled = true; 
        try {
            const response = await fetch('/api/log/start', { method: 'POST' });
            const data = await response.json();
            if (data.status === 'started' || data.status === 'already_running') {
                logServerIsRunning = true; 
            }
        } catch (error) {
            console.error('JS ERROR: Error starting log server:', error);
        } finally {
            updateLogServerButtons(); 
        }
    });

    stopLogButton.addEventListener('click', async () => {
        stopLogButton.disabled = true; 
        try {
            const response = await fetch('/api/log/stop', { method: 'POST' });
            const data = await response.json();
            if (data.status === 'stopped' || data.status === 'not_running') {
                logServerIsRunning = false; 
            }
        } catch (error) {
            console.error('JS ERROR: Error stopping log server:', error);
        } finally {
            updateLogServerButtons(); 
        }
    });

    clearLogButton.addEventListener('click', () => {
        logOutput.innerHTML = ''; 
    });

    // 新增：侧边栏切换按钮的事件监听器
    toggleSidePanelButton.addEventListener('click', () => {
        isSidePanelCollapsed = !isSidePanelCollapsed;
        applySidePanelState();
        saveSidePanelState();
    });

    // === Initial Setup on Page Load ===
    updateDeviceControlButtons(); 
    fetchScannerStatus(); 
    fetchLogServerStatus(); 
    loadSidePanelState(); // 页面加载时加载侧边栏状态
});
