// ./static/script.js

document.addEventListener('DOMContentLoaded', () => {
    // 显示加载指示器
    function showLoading(element) {
        if (element) {
            element.style.display = 'inline-block';
        }
    }

    // 隐藏加载指示器
    function hideLoading(element) {
        if (element) {
            element.style.display = 'none';
        }
    }

    // 设置加载状态
    function setLoadingState(element, isLoading) {
        if (isLoading) {
            showLoading(element);
        } else {
            hideLoading(element);
        }
    }

    function updateButtonStates(config) {
        Object.keys(config).forEach(buttonId => {
            const button = document.getElementById(buttonId);
            if (button) {
                button.disabled = config[buttonId];
            }
        });
    }

    async function apiRequest(url, options = {}, loadingIndicator = null) {
        setLoadingState(loadingIndicator, true);
        try {
            const response = await fetch(url, options);
            const data = await response.json();
            if (!response.ok) {
                throw new Error(data.message || 'Request failed');
            }
            return data;
        } catch (error) {
            console.error('JS ERROR:', error);
            throw error;
        } finally {
            setLoadingState(loadingIndicator, false);
        }
    }

    const scanButton = document.getElementById('scanButton');
    const deviceList = document.getElementById('deviceList');

    const commandInput = document.getElementById('commandInput');
    const sendCommandButton = document.getElementById('sendCommandButton');
    const commandHistoryList = document.getElementById('commandHistoryList');
    const commandSendingIndicator = document.getElementById('commandSendingIndicator');

    const startLogButton = document.getElementById('startLogButton');
    const stopLogButton = document.getElementById('stopLogButton');
    const clearLogButton = document.getElementById('clearLogButton');
    const logOutput = document.getElementById('logOutput');
    const logLoadingIndicator = document.getElementById('logLoadingIndicator');

    const toggleSidePanelButton = document.getElementById('toggleSidePanelButton');
    const sidePanel = document.querySelector('.side-panel');

    const errorMessage = document.getElementById('errorMessage');

    let currentDevices = [];
    let selectedDeviceIp = null;  // Track selected device from list
    let logServerIsRunning = false;
    let isManualScanInProgress = false;
    let isSingleScanInProgress = false;
    let isSidePanelCollapsed = false;
    const SIDE_PANEL_STATE_KEY = 'probe_tool_side_panel_collapsed';

    // 防抖函数
    function debounce(func, wait) {
        let timeout;
        return function(...args) {
            clearTimeout(timeout);
            timeout = setTimeout(() => func.apply(this, args), wait);
        };
    }

    function showError(message) {
        errorMessage.textContent = '';
        const span = document.createElement('span');
        span.textContent = message;
        const closeBtn = document.createElement('button');
        closeBtn.className = 'close-btn';
        closeBtn.setAttribute('aria-label', 'Close error message');
        closeBtn.textContent = '×';
        closeBtn.addEventListener('click', () => {
            errorMessage.style.display = 'none';
        });
        errorMessage.appendChild(span);
        errorMessage.appendChild(closeBtn);
        errorMessage.style.display = 'block';

        setTimeout(() => {
            errorMessage.style.display = 'none';
        }, 3000);
    }


    let wsDevices = null;
    let wsLog = null;
    const RECONNECT_DELAY = 3000;

    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${wsProtocol}//${window.location.hostname}:8001`;

    const debouncedUpdateDeviceUI = debounce(() => {
        updateDeviceList(currentDevices);
        updateDeviceSelect(currentDevices);
        updateDiscoveredDevicesCount(currentDevices.length);
    }, 200);

    function connectDeviceWebSocket() {
        // Attempting to connect Device WebSocket...
        wsDevices = new WebSocket(wsUrl);

        wsDevices.onopen = () => {
            // Connected to Device WebSocket. Sending registration...
            wsDevices.send(JSON.stringify({ type: 'devices' }));
            fetchLogServerStatus();
        };

        wsDevices.onmessage = (event) => {
            try {
                const message = JSON.parse(event.data);
                if (message.type === 'devices') {
                    const newDevices = message.data;

                    currentDevices = newDevices;

                    if (isManualScanInProgress) {
                        updateDeviceList(currentDevices);
                        updateDeviceSelect(currentDevices);
                        isManualScanInProgress = false;
                        isSingleScanInProgress = false;
                    } else {
                        debouncedUpdateDeviceUI();
                    }

                    updateScannerButtons();
                } else if (message.type === 'info') {
                    console.info('JS INFO: Device WS Info:', message.data);
                }
            } catch (e) {
                console.error('JS ERROR: Error parsing device WS message:', e, event.data);
            }
        };

        wsDevices.onclose = (event) => {
            // Device WebSocket closed. Reconnecting...
            setTimeout(connectDeviceWebSocket, RECONNECT_DELAY);
        };

        wsDevices.onerror = (error) => {
            console.error('JS ERROR: Device WebSocket error:', error);
        };
    }

    function connectLogWebSocket() {
        // Attempting to connect Log WebSocket...
        wsLog = new WebSocket(wsUrl);

        wsLog.onopen = () => {
            // Connected to Log WebSocket
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
                    // 转义HTML特殊字符以防止XSS
                    logLine.textContent = message.data;
                    
                    const scrollTolerance = 5; 
                    const isScrolledToBottom = (logOutput.scrollHeight - logOutput.clientHeight) <= (logOutput.scrollTop + scrollTolerance);

                    // 优化：限制日志行数，防止内存泄漏
                    if (logOutput.children.length > 1000) {
                        logOutput.removeChild(logOutput.firstChild);
                    }
                    
                    logOutput.appendChild(logLine); 

                    if (isScrolledToBottom) {
                        // On mobile devices, ensure smooth scrolling to bottom
                        if (window.innerWidth <= 768) {
                            // Use requestAnimationFrame for smoother mobile scrolling
                            requestAnimationFrame(() => {
                                logOutput.scrollTop = logOutput.scrollHeight;
                            });
                        } else {
                            logOutput.scrollTop = logOutput.scrollHeight;
                        }
                    }
                }
            } catch (e) {
                // 忽略非JSON日志或解析错误
            }
        };

        wsLog.onclose = (event) => {
            // Log WebSocket disconnected. Reconnecting...
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
            spanDelete.textContent = '×';
            spanDelete.title = 'Remove from history';
            spanDelete.addEventListener('click', (e) => deleteHistoryItem(e, index));

            li.appendChild(spanText);
            li.appendChild(spanDelete);
            commandHistoryList.appendChild(li);
        });
    }

    loadHistory();

    commandInput.addEventListener('focus', (e) => {
        if (commandHistory.length > 0 && selectedDeviceIp) {
            renderHistoryList();
            commandHistoryList.style.display = 'block';
        }
    });

    commandInput.addEventListener('blur', (e) => {
        // 延迟隐藏历史列表，让用户有机会点击历史项
        setTimeout(() => {
            // 检查当前焦点是否仍在输入框或历史列表上
            if (!commandInput.contains(document.activeElement) &&
                !commandHistoryList.contains(document.activeElement)) {
                commandHistoryList.style.display = 'none';
            }
        }, 300); // 300ms延迟，兼容触摸板（触摸板点击比鼠标慢）
    });

    document.addEventListener('click', (e) => {
        if (!commandInput.contains(e.target) && !commandHistoryList.contains(e.target)) {
            commandHistoryList.style.display = 'none';
        }
    });

    // 当用户选择历史项时
    commandHistoryList.addEventListener('click', () => {
        commandHistoryList.style.display = 'none';
    });

    // === UI 更新函数 ===

    function updateDeviceControlButtons() {
        const selectedIp = selectedDeviceIp;
        const selectedDevice = currentDevices.find(d => d.ip === selectedIp);
        const isConnected = selectedDevice && selectedDevice.status === 'Connected';

        document.querySelectorAll('#deviceList li').forEach(li => {
            if (li.dataset.ip === selectedIp) {
                const connectBtn = li.querySelector('.device-connect-btn');
                const disconnectBtn = li.querySelector('.device-disconnect-btn');
                const connectionInfoEl = li.querySelector('.device-connection-info');

                if (connectBtn) {
                    connectBtn.disabled = !selectedIp || isConnected;
                }
                if (disconnectBtn) {
                    disconnectBtn.disabled = !selectedIp || !isConnected;
                }

                if (connectionInfoEl && isConnected && selectedDevice.connected_via) {
                    connectionInfoEl.textContent = `Via Local IP: ${selectedDevice.connected_via}`;
                    connectionInfoEl.style.color = '#006400';
                } else if (connectionInfoEl && selectedDevice && selectedDevice.status === 'Available') {
                    connectionInfoEl.textContent = 'Device Available';
                    connectionInfoEl.style.color = '#666';
                } else if (connectionInfoEl) {
                    connectionInfoEl.textContent = '';
                }
            }
        });

        if (sendCommandButton) {
            sendCommandButton.disabled = !isConnected;
        }

        if (isConnected) {
            commandInput.focus();
        }
    }

    function updateScannerButtons() {
        updateButtonStates({
            'scanButton': isSingleScanInProgress
        });
    }

    function updateLogServerButtons() {
        updateButtonStates({
            'startLogButton': logServerIsRunning,
            'stopLogButton': !logServerIsRunning
        });
    }

    function updateDiscoveredDevicesCount(count) {
        const countElement = document.getElementById('discoveredDevicesCount');
        if (countElement) {
            countElement.textContent = `${count} ${count === 1 ? 'device' : 'devices'}`;
        }
    }

    function updateDeviceList(devices) {
        deviceList.innerHTML = '';
        if (!devices || devices.length === 0) {
            deviceList.innerHTML = '<li>No devices found. Click "Refresh Devices" to scan.</li>';
            updateDiscoveredDevicesCount(0);
            selectedDeviceIp = null;
            updateDeviceControlButtons();
            return;
        }

        const isSelectedDevicePresent = devices.some(device => device.ip === selectedDeviceIp);
        if (selectedDeviceIp && !isSelectedDevicePresent) {
            selectedDeviceIp = null;
        }

        devices.forEach(device => {
            if (device.ip && device.id !== undefined && device.id !== null) {
                const li = document.createElement('li');
                li.dataset.ip = device.ip;

                const deviceInfoDiv = document.createElement('div');
                deviceInfoDiv.className = 'device-info';
                deviceInfoDiv.textContent = `${device.ip}-[${device.id}] Status: ${device.status}`;

                li.appendChild(deviceInfoDiv);

                const controlDiv = document.createElement('div');
                controlDiv.className = 'device-control-div';
                controlDiv.style.display = 'none';

                const btnGroup = document.createElement('div');
                btnGroup.className = 'button-group';
                const connectBtn = document.createElement('button');
                connectBtn.className = 'device-connect-btn';
                connectBtn.setAttribute('aria-label', 'Connect to selected device');
                connectBtn.textContent = 'Connect';
                const disconnectBtn = document.createElement('button');
                disconnectBtn.className = 'device-disconnect-btn';
                disconnectBtn.setAttribute('aria-label', 'Disconnect from device');
                disconnectBtn.textContent = 'Disconnect';
                const connLoading = document.createElement('div');
                connLoading.className = 'device-connection-loading loading-indicator';
                connLoading.style.display = 'none';
                connLoading.setAttribute('aria-live', 'polite');
                connLoading.textContent = 'Connecting...';
                btnGroup.appendChild(connectBtn);
                btnGroup.appendChild(disconnectBtn);
                btnGroup.appendChild(connLoading);
                const connInfo = document.createElement('p');
                connInfo.className = 'device-connection-info info-text';
                controlDiv.appendChild(btnGroup);
                controlDiv.appendChild(connInfo);

                li.appendChild(controlDiv);

                // 设备信息区域点击事件 - 用于选择设备
                deviceInfoDiv.addEventListener('click', function(event) {
                    event.stopPropagation();

                    if (selectedDeviceIp === device.ip) {
                        // 取消选择当前设备
                        selectedDeviceIp = null;
                        li.classList.remove('selected');
                        controlDiv.style.display = 'none';
                    } else {
                        // 选择新设备
                        // 移除所有选中高亮
                        document.querySelectorAll('#deviceList li').forEach(item => {
                            item.classList.remove('selected');
                        });
                        // 隐藏所有控制按钮
                        document.querySelectorAll('#deviceList .device-control-div').forEach(div => {
                            div.style.display = 'none';
                        });

                        // 选中当前设备
                        li.classList.add('selected');
                        selectedDeviceIp = device.ip;
                        controlDiv.style.display = 'block';
                    }

                    updateDeviceControlButtons();
                });

                // 控制区域点击事件 - 阻止冒泡，不触发设备选择
                controlDiv.addEventListener('click', function(event) {
                    // 如果点击的是控制按钮，让按钮事件处理器处理
                    if (event.target.classList.contains('device-connect-btn') ||
                        event.target.classList.contains('device-disconnect-btn') ||
                        event.target.classList.contains('device-connection-loading') ||
                        event.target.classList.contains('device-connection-info')) {
                        return;
                    }
                    // 其他控制区域点击，阻止冒泡
                    event.stopPropagation();
                });

                if (selectedDeviceIp === device.ip) {
                    li.classList.add('selected');
                    setTimeout(() => {
                        const controlDiv = li.querySelector('.device-control-div');
                        if (controlDiv) {
                            controlDiv.style.display = 'block';
                        }
                    }, 0);
                }

                deviceList.appendChild(li);
            }
        });

        updateDiscoveredDevicesCount(devices.length);
    }

    function updateDeviceSelect(devices) {
        document.querySelectorAll('#deviceList li').forEach(item => {
            if (item.dataset.ip === selectedDeviceIp) {
                item.classList.add('selected');
            } else {
                item.classList.remove('selected');
            }
        });

        updateDeviceControlButtons();
    }

    async function fetchLogServerStatus() {
        try {
            const response = await fetch('/api/log/status');
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
            toggleSidePanelButton.textContent = 'Expand panel';
            toggleSidePanelButton.setAttribute('aria-expanded', 'false');
        } else {
            sidePanel.classList.remove('collapsed');
            toggleSidePanelButton.textContent = 'Collapse panel';
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
    
    // 添加键盘快捷键支持
    document.addEventListener('keydown', (event) => {
        // Ctrl+Enter 发送命令
        if (event.ctrlKey && event.key === 'Enter' && !sendCommandButton.disabled) {
            event.preventDefault();
            sendCommandButton.click();
        }
        
        // Escape键关闭错误提示
        if (event.key === 'Escape') {
            if (errorMessage.style.display === 'block') {
                errorMessage.style.display = 'none';
            }
        }
    });

    scanButton.addEventListener('click', async () => {
        scanButton.disabled = true;
        deviceList.innerHTML = '<li>Scanning for devices (5 seconds)...</li>';

        currentDevices = [];

        isManualScanInProgress = true;
        isSingleScanInProgress = true;
        updateScannerButtons();

        try {
            await apiRequest('/api/scanner/refresh', { method: 'POST' });
        } catch (error) {
            console.error('JS ERROR: Error initiating device list refresh:', error);
            deviceList.innerHTML = '<li>Error initiating scan.</li>';
            showError('Failed to initiate device scan: ' + error.message);
            isManualScanInProgress = false;
            isSingleScanInProgress = false;
            updateScannerButtons();
        } finally {
            updateScannerButtons();
        }
    });

    // Removed deviceSelect change event listener as the dropdown has been removed
    // Device selection now happens through clicking on the device list items

    // 使用事件委托处理动态添加的按钮
    deviceList.addEventListener('click', async (event) => {
        // 处理连接按钮点击
        if (event.target.classList.contains('device-connect-btn')) {
            const ip = selectedDeviceIp;
            if (!ip) return;

            // 获取当前设备项的加载指示器元素
            const deviceItem = event.target.closest('li');
            const loadingIndicator = deviceItem.querySelector('.device-connection-loading');
            if (loadingIndicator) {
                loadingIndicator.style.display = 'inline-block';
            }

            // 禁用连接按钮
            event.target.disabled = true;

            try {
                await apiRequest('/api/connect', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ ip })
                }, null); // 不使用全局加载指示器

                // 重新启用断开按钮
                const disconnectBtn = deviceItem.querySelector('.device-disconnect-btn');
                if (disconnectBtn) {
                    disconnectBtn.disabled = false;
                }
            } catch (error) {
                console.error(`JS ERROR: Error connecting to ${ip}:`, error);
                showError(`Failed to connect to device ${ip}: ` + error.message);
            } finally {
                // 隐藏加载指示器
                if (loadingIndicator) {
                    loadingIndicator.style.display = 'none';
                }
                // 重新启用连接按钮
                event.target.disabled = false;
            }
        }
        // 处理断开按钮点击
        else if (event.target.classList.contains('device-disconnect-btn')) {
            const ip = selectedDeviceIp;
            if (!ip) return;

            // 获取当前设备项的加载指示器元素
            const deviceItem = event.target.closest('li');
            const loadingIndicator = deviceItem.querySelector('.device-connection-loading');
            if (loadingIndicator) {
                loadingIndicator.style.display = 'inline-block';
            }

            // 禁用断开按钮
            event.target.disabled = true;

            try {
                await apiRequest('/api/disconnect', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ ip })
                }, null); // 不使用全局加载指示器

                // 重新启用连接按钮
                const connectBtn = deviceItem.querySelector('.device-connect-btn');
                if (connectBtn) {
                    connectBtn.disabled = false;
                }
            } catch (error) {
                console.error(`JS ERROR: Error disconnecting from ${ip}:`, error);
                showError(`Failed to disconnect from device ${ip}: ` + error.message);
            } finally {
                // 隐藏加载指示器
                if (loadingIndicator) {
                    loadingIndicator.style.display = 'none';
                }
                // 重新启用断开按钮
                event.target.disabled = false;
            }
        }
    });

    sendCommandButton.addEventListener('click', async () => {
        const ip = selectedDeviceIp;
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
            await apiRequest('/api/send', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ip, fn: fnToSend, stage: stageToSend, data_base64: dataToSendBase64 })
            }, commandSendingIndicator);

            // 清空输入框
            commandInput.value = '';
        } catch (error) {
            console.error(`JS ERROR: Error sending command:`, error);
            showError('Failed to send command: ' + error.message);
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
            const data = await apiRequest('/api/log/start', { method: 'POST' }, logLoadingIndicator);
            if (data.status === 'started' || data.status === 'already_running') {
                logServerIsRunning = true; 
            } else {
                throw new Error(data.message || 'Failed to start log server');
            }
        } catch (error) {
            console.error('JS ERROR: Error starting log server:', error);
            showError('Failed to start log server: ' + error.message);
        } finally {
            updateLogServerButtons(); 
        }
    });

    stopLogButton.addEventListener('click', async () => {
        stopLogButton.disabled = true; 
        try {
            const data = await apiRequest('/api/log/stop', { method: 'POST' }, logLoadingIndicator);
            if (data.status === 'stopped' || data.status === 'not_running') {
                logServerIsRunning = false; 
            } else {
                throw new Error(data.message || 'Failed to stop log server');
            }
        } catch (error) {
            console.error('JS ERROR: Error stopping log server:', error);
            showError('Failed to stop log server: ' + error.message);
        } finally {
            updateLogServerButtons(); 
        }
    });

    clearLogButton.addEventListener('click', () => {
        logOutput.innerHTML = ''; 
    });
    
    // Improve mobile touch scrolling for log area
    let touchStartY = 0;
    logOutput.addEventListener('touchstart', (e) => {
        touchStartY = e.touches[0].clientY;
    }, { passive: true });
    
    logOutput.addEventListener('touchmove', (e) => {
        const touchY = e.touches[0].clientY;
        const scrollTop = logOutput.scrollTop;
        const scrollHeight = logOutput.scrollHeight;
        const clientHeight = logOutput.clientHeight;
        
        // Allow native scrolling when content overflows
        if (scrollHeight > clientHeight) {
            // Prevent pull-to-refresh interference at top
            if (scrollTop === 0 && touchY > touchStartY) {
                e.preventDefault();
            }
            // Prevent pull-to-refresh interference at bottom
            else if (scrollTop + clientHeight >= scrollHeight && touchY < touchStartY) {
                e.preventDefault();
            }
        }
    }, { passive: false });

    // 新增：侧边栏切换按钮的事件监听器
    toggleSidePanelButton.addEventListener('click', () => {
        isSidePanelCollapsed = !isSidePanelCollapsed;
        applySidePanelState();
        saveSidePanelState();
    });

    // === Initial Setup on Page Load ===
    updateDeviceControlButtons();
    updateDeviceList(currentDevices); // 初始化设备列表
    updateDeviceSelect(currentDevices); // 初始化设备选择
    updateDiscoveredDevicesCount(currentDevices.length); // 初始化设备计数
    fetchLogServerStatus();
    loadSidePanelState(); // 页面加载时加载侧边栏状态
    
    // 添加窗口大小变化的防抖处理
    const handleResize = debounce(() => {
        // 在窗口大小变化时重新计算布局
        if (logOutput.scrollHeight - logOutput.clientHeight <= logOutput.scrollTop + 5) {
            logOutput.scrollTop = logOutput.scrollHeight;
        }
        
        // Mobile-specific adjustments
        if (window.innerWidth <= 768) {
            // Force scroll to bottom on mobile resize if near bottom
            if (logOutput.scrollHeight - logOutput.clientHeight <= logOutput.scrollTop + 20) {
                setTimeout(() => {
                    logOutput.scrollTop = logOutput.scrollHeight;
                }, 100);
            }
        }
    }, 250);
    
    window.addEventListener('resize', handleResize);
});