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
    
    // Send Command (Text) Section
    const commandInput = document.getElementById('commandInput');
    const sendCommandButton = document.getElementById('sendCommandButton');
    
    // Log Stream Section
    const startLogButton = document.getElementById('startLogButton');
    const stopLogButton = document.getElementById('stopLogButton');
    const clearLogButton = document.getElementById('clearLogButton'); // Clear Log button
    const logOutput = document.getElementById('logOutput');

    // FTP Server Control Section (已移除前端元素，所以无需获取其引用)
    // const startFtpButton = document.getElementById('startFtpButton');
    // const stopFtpButton = document.getElementById('stopFtpButton');
    // const ftpStatusText = document.getElementById('ftpStatusText');

    // === 内部状态变量 ===
    let currentDevices = []; // To store the latest device snapshot
    let scannerIsRunning = false; // To track the state of the *continuous* scanner (started/stopped)
    let logServerIsRunning = false; // To track the state of the log server (started/stopped)


    // === UI 更新函数 ===

    /**
     * 更新设备控制按钮（Connect, Disconnect, Send Command）的状态
     * 基于当前选中的设备及其连接状态。
     */
    function updateDeviceControlButtons() {
        console.log('JS DEBUG: updateDeviceControlButtons called.');
        const selectedIp = deviceSelect.value;
        const selectedDevice = currentDevices.find(d => d.ip === selectedIp);
        const isConnected = selectedDevice && selectedDevice.status === 'Connected';

        connectButton.disabled = !selectedIp || isConnected;
        disconnectButton.disabled = !selectedIp || !isConnected;
        sendCommandButton.disabled = !isConnected; 
        console.log(`JS DEBUG: Buttons updated. Selected IP: ${selectedIp}, Connected: ${isConnected}`);
    }

    /**
     * 更新扫描器控制按钮（Start/Stop Scanner, Refresh Devices）的状态。
     */
    function updateScannerButtons() {
        startScannerButton.disabled = scannerIsRunning;
        stopScannerButton.disabled = !scannerIsRunning;
        // "Refresh Devices" button is disabled ONLY when the continuous scanner is running
        scanButton.disabled = scannerIsRunning; 
        console.log(`JS DEBUG: Scanner buttons updated. Continuous scanner running: ${scannerIsRunning}. Refresh Devices disabled: ${scanButton.disabled}`);
    }

    /**
     * 更新日志服务器控制按钮（Start/Stop Log）的状态。
     */
    function updateLogServerButtons() {
        startLogButton.disabled = logServerIsRunning;
        stopLogButton.disabled = !logServerIsRunning;
        console.log(`JS DEBUG: Log Server buttons updated. Running: ${logServerIsRunning}`);
    }

    /**
     * 根据设备列表数据更新前端的设备列表 `<ul>`。
     * @param {Array<Object>} devices - 包含设备信息的数组。
     */
    function updateDeviceList(devices) {
        console.log('JS DEBUG: updateDeviceList called with devices:', devices);
        deviceList.innerHTML = ''; // Clear current list
        if (!devices || devices.length === 0) {
            deviceList.innerHTML = '<li>No devices found. Start scanner and click "Refresh Devices"</li>';
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
     * @param {Array<Object>} devices - 包含设备信息的数组。
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
            scannerIsRunning = false; // Assume stopped if error
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
            logServerIsRunning = false; // Assume stopped if error
            updateLogServerButtons();
        }
    }


    // === WebSocket Handlers ===

    // Device WebSocket: Handles real-time device list updates
    wsDevices.onopen = () => {
        console.log('JS DEBUG: Connected to device WebSocket. Sending registration...');
        wsDevices.send(JSON.stringify({ type: 'devices' })); 
        console.log('JS DEBUG: Sent device WebSocket registration message.');
        // 页面加载后立即同步所有服务状态
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

    // Log WebSocket: Handles real-time log stream
    wsLog.onopen = () => {
        console.log('JS DEBUG: Connected to log WebSocket');
        wsLog.send(JSON.stringify({ type: 'log' })); // Register for log updates
    };

    wsLog.onmessage = (event) => {
        const message = JSON.parse(event.data);
        if (message.type === 'log') {
            const logLine = document.createElement('div');
            if (message.data.startsWith('CMD_RESP from')) {
                logLine.className = 'command-response'; 
            }
            logLine.textContent = message.data;
            logOutput.prepend(logLine); // prepend: Newest logs at the top
        }
    };

    wsLog.onclose = (event) => {
        console.log('JS DEBUG: Log WebSocket disconnected. Code:', event.code, 'Reason:', event.reason);
    };

    wsLog.onerror = (error) => {
        console.error('JS ERROR: Log WebSocket error:', error);
    };


    // === Event Listeners ===

    // Device Discovery Buttons
    startScannerButton.addEventListener('click', async () => {
        startScannerButton.disabled = true; 
        try {
            const response = await fetch('/api/scanner/start', { method: 'POST' });
            const data = await response.json();
            console.log('JS DEBUG: Continuous scanner start initiated:', data);
            if (data.status === 'scanner_started' || data.status === 'scanner_already_running') {
                scannerIsRunning = true;
            }
        } catch (error) {
            console.error('JS ERROR: Error starting continuous scanner:', error);
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
        // scanButton is already disabled if continuous scanner is running, no need for another check here.
        scanButton.disabled = true; // Disable "Refresh Devices" temporarily while it's performing its scan
        try {
            const response = await fetch('/api/scan', { method: 'POST' });
            const data = await response.json();
            console.log('JS DEBUG: Device list refresh/timed scan initiated:', data);
            // Re-enable the button after the backend has acknowledged the start.
            // The timed scan runs for 5 seconds, but frontend can re-enable immediately to allow re-triggering.
        } catch (error) {
            console.error('JS ERROR: Error initiating device list refresh:', error);
        } finally {
            // Re-enable only if the continuous scanner is NOT running
            scanButton.disabled = scannerIsRunning; 
        }
    });

    // Device Control Buttons
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

    // Send Command (Text) Button Logic
    sendCommandButton.addEventListener('click', async () => {
        const ip = deviceSelect.value;
        const text = commandInput.value;
        if (!ip || !text) return; 

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

    // Log Server Buttons
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

    // === Clear Log Button ===
    clearLogButton.addEventListener('click', () => {
        logOutput.innerHTML = ''; 
        console.log('JS DEBUG: Log output cleared.');
    });


    // === Initial Setup on Page Load ===
    // These functions now reflect the auto-start behavior on the backend.
    // The UI will fetch the actual state from the backend via API calls (fetchScannerStatus, fetchLogServerStatus)
    // and WebSockets will keep the device list updated.
    updateDeviceControlButtons(); 
    fetchScannerStatus(); 
    fetchLogServerStatus(); 
});
