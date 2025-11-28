// ./static/script.js

document.addEventListener('DOMContentLoaded', () => {
    // === WebSocket 连接设置 ===
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsLog = new WebSocket(`${wsProtocol}//${window.location.hostname}:8001`); // Log WebSocket
    const wsDevices = new WebSocket(`${wsProtocol}//${window.location.hostname}:8001`); // Device WebSocket

    // === 获取所有前端元素 ===
    // Device Discovery Section
    const startScannerButton = document.getElementById('startScannerButton');
    const stopScannerButton = document.getElementById('stopScannerButton');
    const scanButton = document.getElementById('scanButton'); // "Refresh Devices" button
    const deviceList = document.getElementById('deviceList');

    // Device Control Section
    const deviceSelect = document.getElementById('deviceSelect');
    const connectButton = document.getElementById('connectButton');
    const disconnectButton = document.getElementById('disconnectButton');
    const connectionInfo = document.getElementById('connectionInfo'); // 新增
    
    // Send Command (Text) Section
    const commandInput = document.getElementById('commandInput');
    const sendCommandButton = document.getElementById('sendCommandButton');
    // 新增: 获取历史记录列表元素
    const commandHistoryList = document.getElementById('commandHistoryList');
    
    // Log Stream Section
    const startLogButton = document.getElementById('startLogButton');
    const stopLogButton = document.getElementById('stopLogButton');
    const clearLogButton = document.getElementById('clearLogButton'); 
    const logOutput = document.getElementById('logOutput');

    // === 内部状态变量 ===
    let currentDevices = []; // To store the latest device snapshot
    let scannerIsRunning = false; // To track the state of the *continuous* scanner (started/stopped)
    let logServerIsRunning = false; // To track the state of the log server (started/stopped)

    // === 新增: 历史记录管理逻辑 ===
    const MAX_HISTORY_ITEMS = 10;
    const HISTORY_STORAGE_KEY = 'probe_tool_cmd_history';
    let commandHistory = [];

    // 从 localStorage 加载历史
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

    // 保存历史到 localStorage
    function saveHistory() {
        localStorage.setItem(HISTORY_STORAGE_KEY, JSON.stringify(commandHistory));
    }

    // 添加命令到历史
    function addCommandToHistory(cmd) {
        if (!cmd) return;
        // 如果命令已存在，先移除旧的
        const existingIndex = commandHistory.indexOf(cmd);
        if (existingIndex !== -1) {
            commandHistory.splice(existingIndex, 1);
        }
        // 添加到头部
        commandHistory.unshift(cmd);
        // 限制数量
        if (commandHistory.length > MAX_HISTORY_ITEMS) {
            commandHistory.pop();
        }
        saveHistory();
        renderHistoryList();
    }

    // 删除单条历史
    function deleteHistoryItem(e, index) {
        e.stopPropagation(); // 防止触发 li 的点击事件（即防止填充输入框）
        commandHistory.splice(index, 1);
        saveHistory();
        renderHistoryList();
        // 如果删完了，隐藏列表
        if (commandHistory.length === 0) {
            commandHistoryList.style.display = 'none';
        }
    }

    // 渲染历史列表 UI
    function renderHistoryList() {
        commandHistoryList.innerHTML = '';
        if (commandHistory.length === 0) {
            commandHistoryList.style.display = 'none';
            return;
        }

        commandHistory.forEach((cmd, index) => {
            const li = document.createElement('li');
            
            // 命令文本区
            const spanText = document.createElement('span');
            spanText.className = 'history-text';
            spanText.textContent = cmd;
            
            // 点击文本填充输入框
            li.addEventListener('click', () => {
                commandInput.value = cmd;
                commandHistoryList.style.display = 'none'; // 选择后隐藏列表
            });

            // 删除按钮
            const spanDelete = document.createElement('span');
            spanDelete.className = 'history-delete-btn';
            spanDelete.innerHTML = '&times;'; // X 符号
            spanDelete.title = 'Remove from history';
            spanDelete.addEventListener('click', (e) => deleteHistoryItem(e, index));

            li.appendChild(spanText);
            li.appendChild(spanDelete);
            commandHistoryList.appendChild(li);
        });
    }

    // 初始化加载历史
    loadHistory();

    // 输入框聚焦时显示历史（如果有）
    commandInput.addEventListener('focus', () => {
        if (commandHistory.length > 0) {
            renderHistoryList();
            commandHistoryList.style.display = 'block';
        }
    });

    // 点击页面其他地方隐藏历史列表
    document.addEventListener('click', (e) => {
        if (!commandInput.contains(e.target) && !commandHistoryList.contains(e.target)) {
            commandHistoryList.style.display = 'none';
        }
    });

    // === UI 更新函数 ===

    /**
     * 更新设备控制按钮（Connect, Disconnect, Send Command）的状态
     * 以及下方的连接信息
     */
    function updateDeviceControlButtons() {
        console.log('JS DEBUG: updateDeviceControlButtons called.');
        const selectedIp = deviceSelect.value;
        const selectedDevice = currentDevices.find(d => d.ip === selectedIp);
        const isConnected = selectedDevice && selectedDevice.status === 'Connected';

        connectButton.disabled = !selectedIp || isConnected;
        disconnectButton.disabled = !selectedIp || !isConnected;
        sendCommandButton.disabled = !isConnected; 

        // 更新连接信息文本
        if (isConnected && selectedDevice.connected_via) {
            connectionInfo.textContent = `Via Local IP: ${selectedDevice.connected_via}`;
            connectionInfo.style.color = '#006400'; // 深绿色
        } else if (selectedDevice && selectedDevice.status === 'Available') {
            connectionInfo.textContent = 'Device Available';
            connectionInfo.style.color = '#666';
        } else {
            connectionInfo.textContent = ''; // 清空
        }
        
        console.log(`JS DEBUG: Buttons updated. Selected IP: ${selectedIp}, Connected: ${isConnected}`);
    }

    /**
     * 更新扫描器控制按钮的状态。
     */
    function updateScannerButtons() {
        startScannerButton.disabled = scannerIsRunning;
        stopScannerButton.disabled = !scannerIsRunning;
        scanButton.disabled = scannerIsRunning; 
        console.log(`JS DEBUG: Scanner buttons updated. Continuous scanner running: ${scannerIsRunning}. Refresh Devices disabled: ${scanButton.disabled}`);
    }

    /**
     * 更新日志服务器控制按钮的状态。
     */
    function updateLogServerButtons() {
        startLogButton.disabled = logServerIsRunning;
        stopLogButton.disabled = !logServerIsRunning;
        console.log(`JS DEBUG: Log Server buttons updated. Running: ${logServerIsRunning}`);
    }

    /**
     * 根据设备列表数据更新前端的设备列表 `<ul>`。
     */
    function updateDeviceList(devices) {
        console.log('JS DEBUG: updateDeviceList called with devices:', devices);
        deviceList.innerHTML = ''; // Clear current list
        if (!devices || devices.length === 0) {
            deviceList.innerHTML = '<li>No devices found. Start scanner or click "Refresh Devices"</li>'; 
            console.log('JS DEBUG: No devices to populate list.');
            return;
        }
        devices.forEach(device => {
            if (device.ip && device.id !== undefined && device.id !== null) {
                const li = document.createElement('li');
                li.textContent = `${device.ip}-[${device.id}] Status: ${device.status}`;
                deviceList.appendChild(li);
                console.log(`JS DEBUG: Added list item for IP: ${device.ip}, ID: '${device.id}'`);
            } else {
                console.warn(`JS WARNING: updateDeviceList skipping device with invalid IP or ID. Device object:`, device);
            }
        });
    }

    /**
     * 根据设备列表数据更新前端的设备选择下拉菜单 `<select>`。
     */
    function updateDeviceSelect(devices) {
        console.log('JS DEBUG: updateDeviceSelect called with devices:', devices);
        const selectedIp = deviceSelect.value; // Remember currently selected IP
        deviceSelect.innerHTML = '<option value="">-- Select a device --</option>'; // Clear and add default option
        if (!devices || devices.length === 0) {
            console.log('JS DEBUG: No devices to populate select dropdown.');
            updateDeviceControlButtons(); // Still update control buttons even if no devices
            return;
        }
        devices.forEach(device => {
            if (device.ip && device.id !== undefined && device.id !== null) {
                const option = document.createElement('option');
                option.value = device.ip;
                option.textContent = `${device.id} (${device.ip}) - ${device.status}`;
                if (device.ip === selectedIp) { // Restore selection if device still exists
                    option.selected = true;
                }
                deviceSelect.appendChild(option);
                console.log(`JS DEBUG: Added option for IP: ${device.ip}, ID: '${device.id}'`);
            } else {
                console.warn(`JS WARNING: updateDeviceSelect skipping device with invalid IP or ID. Device object:`, device);
            }
        });
        updateDeviceControlButtons(); // Update control buttons after select is populated
    }

    /**
     * 从后端API获取当前扫描器的运行状态并更新UI。
     */
    async function fetchScannerStatus() {
        try {
            const response = await fetch('/api/scanner_status');
            const data = await response.json();
            scannerIsRunning = (data.scanner_status === 'running');
            updateScannerButtons();
            console.log(`JS DEBUG: Fetched continuous scanner status: ${data.scanner_status}, scannerIsRunning: ${scannerIsRunning}`);
        } catch (error) {
            console.error('JS ERROR: Error fetching scanner status:', error);
            scannerIsRunning = false; 
            updateScannerButtons();
        }
    }

    /**
     * 从后端API获取当前日志服务器的运行状态并更新UI。
     */
    async function fetchLogServerStatus() {
        try {
            const response = await fetch('/api/log_server_status');
            const data = await response.json();
            logServerIsRunning = (data.log_server_status === 'running');
            updateLogServerButtons();
            console.log(`JS DEBUG: Fetched log server status: ${data.log_server_status}, logServerIsRunning: ${logServerIsRunning}`);
        } catch (error) {
            console.error('JS ERROR: Error fetching log server status:', error);
            logServerIsRunning = false; 
            updateLogServerButtons();
        }
    }


    // === WebSocket Handlers ===

    wsDevices.onopen = () => {
        console.log('JS DEBUG: Connected to device WebSocket. Sending registration...');
        wsDevices.send(JSON.stringify({ type: 'devices' })); 
        console.log('JS DEBUG: Sent device WebSocket registration message.');
        fetchScannerStatus(); 
        fetchLogServerStatus(); 
    };

    wsDevices.onmessage = (event) => {
        console.log('JS DEBUG: Received message on device WS:', event.data);
        try {
            const message = JSON.parse(event.data);
            console.log('JS DEBUG: Parsed device message:', message);
            if (message.type === 'devices') {
                currentDevices = message.data;
                console.log('JS DEBUG: currentDevices updated:', currentDevices);
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
        console.log('JS DEBUG: Device WebSocket disconnected. Code:', event.code, 'Reason:', event.reason);
    };

    wsDevices.onerror = (error) => {
        console.error('JS ERROR: Device WebSocket error:', error);
    };

    wsLog.onopen = () => {
        console.log('JS DEBUG: Connected to log WebSocket');
        wsLog.send(JSON.stringify({ type: 'log' })); 
    };

    wsLog.onmessage = (event) => {
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
    };

    wsLog.onclose = (event) => {
        console.log('JS DEBUG: Log WebSocket disconnected. Code:', event.code, 'Reason:', event.reason);
    };

    wsLog.onerror = (error) => {
        console.error('JS ERROR: Log WebSocket error:', error);
    };


    // === Event Listeners ===

    startScannerButton.addEventListener('click', async () => {
        startScannerButton.disabled = true; 
        deviceList.innerHTML = '<li>Scanning for devices (continuous)...</li>'; 
        deviceSelect.innerHTML = '<option value="">-- Select a device --</option>'; 
        currentDevices = []; 

        try {
            const response = await fetch('/api/scanner/start', { method: 'POST' });
            const data = await response.json();
            console.log('JS DEBUG: Continuous scanner start initiated:', data);
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
            console.log('JS DEBUG: Continuous scanner stop initiated:', data);
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
            const data = await response.json();
            console.log('JS DEBUG: Device list refresh/timed scan initiated:', data);
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
            const response = await fetch('/api/connect', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ip })
            });
            const data = await response.json();
            console.log(`JS DEBUG: Connect to ${ip}:`, data);
        } catch (error) {
            console.error(`JS ERROR: Error connecting to ${ip}:`, error);
        } finally {
            // UI will be updated via WebSocket push
        }
    });

    disconnectButton.addEventListener('click', async () => {
        const ip = deviceSelect.value;
        if (!ip) return; 

        disconnectButton.disabled = true; 
        try {
            const response = await fetch('/api/disconnect', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ip })
            });
            const data = await response.json();
            console.log(`JS DEBUG: Disconnect from ${ip}:`, data);
        } catch (error) {
            console.error(`JS ERROR: Error disconnecting from ${ip}:`, error);
        } finally {
            // UI will be updated via WebSocket push
        }
    });

    sendCommandButton.addEventListener('click', async () => {
        const ip = deviceSelect.value;
        const text = commandInput.value;
        if (!ip || !text) return; 

        // === 新增: 保存到历史 ===
        addCommandToHistory(text);
        // =======================

        sendCommandButton.disabled = true; 
        
        let fnToSend = 0; 
        const stageToSend = 0; 
        const dataToSendRaw = text; 

        const match = text.match(/^(\d+)/); 
        if (match && match[1]) {
            fnToSend = parseInt(match[1], 10);
        } else {
            fnToSend = 0; 
        }

        const dataToSendBase64 = btoa(unescape(encodeURIComponent(dataToSendRaw)));

        try {
            const response = await fetch('/api/send', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ip, fn: fnToSend, stage: stageToSend, data_base64: dataToSendBase64 })
            });
            const data = await response.json();
            console.log(`JS DEBUG: Sent protocol command (fn=${fnToSend}, stage=${stageToSend}, data='${dataToSendRaw}') to ${ip}:`, data);
        } catch (error) {
            console.error(`JS ERROR: Error sending command (fn=${fnToSend}, stage=${stageToSend}, data='${dataToSendRaw}') to ${ip}:`, error);
        } finally {
            sendCommandButton.disabled = false; 
        }
    });

    // === 允许在输入框按 Enter 键触发发送 (并保存历史) ===
    commandInput.addEventListener('keydown', (event) => {
        if (event.key === 'Enter') {
            event.preventDefault(); // 防止默认换行或其他行为
            if (!sendCommandButton.disabled) {
                // 直接复用 sendCommandButton 的点击逻辑，确保逻辑一致
                sendCommandButton.click();
            }
        }
    });

    startLogButton.addEventListener('click', async () => {
        startLogButton.disabled = true; 
        try {
            const response = await fetch('/api/log/start', { method: 'POST' });
            const data = await response.json();
            console.log('JS DEBUG: Log server start initiated:', data);
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
            console.log('JS DEBUG: Log server stop initiated:', data);
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
        console.log('JS DEBUG: Log output cleared.');
    });

    // === Initial Setup on Page Load ===
    updateDeviceControlButtons(); 
    fetchScannerStatus(); 
    fetchLogServerStatus(); 
});
