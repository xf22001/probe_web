# ./app.py
import os
import json
import threading
import asyncio
from http.server import SimpleHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse
from typing import Dict, Any, Optional
import websockets
import traceback
import logging

# 导入自定义模块
from scanner import UdpDiscoveryServer, BROADCAST_PORT
from connector import Connector, PROBE_TOOL_PORT
# 修正：显式导入 base64_to_bytes
from protocol import encode_request, text_to_request_bytes, base64_to_bytes, decode_request 
from logger_server import UdpLogServer, LOG_TOOL_PORT
from ftp_server import ProbeFtpServer # 导入 FTP 服务器类

# HTTP 和 WebSocket 端口保持不变
HTTP_PORT = 8000
WS_PORT = 8001

logger = logging.getLogger(__name__)

class ServerState:
    def __init__(self, loop: asyncio.AbstractEventLoop):
        self.loop = loop
        self.devices_lock = threading.Lock()
        self.devices: Dict[str, Dict[str, Any]] = {}  # ip -> {id:, status:}
        
        self.connector = Connector(loop=self.loop, on_command_response=self.handle_command_response) 
        
        self.log_server: Optional[UdpLogServer] = None
        self.continuous_discovery_server: Optional[UdpDiscoveryServer] = None # For "Start Scanner" (continuous)
        self.active_timed_scanner: Optional[UdpDiscoveryServer] = None # For "Refresh Devices" (5-second scan)
        self.active_timed_scan_task: Optional[asyncio.Task] = None # To manage cancellation of timed scan
        self.ftp_server: Optional[ProbeFtpServer] = None # FTP server will be initialized and started externally
        
        self.log_ws = set()
        self.dev_ws = set()

    def update_devices(self, found_ip: str, device_id: str):
        logger.debug(f"update_devices called. IP: {found_ip}, Raw ID: '{device_id}'")
        with self.devices_lock:
            display_device_id = device_id if device_id else f"Unnamed_Device_{found_ip.replace('.', '_')}"
            
            existing = self.devices.get(found_ip)
            if not existing:
                self.devices[found_ip] = {"id": display_device_id, "status": "Available"}
                logger.info(f"Added new device: {found_ip} -> {self.devices[found_ip]}")
            else:
                if existing["id"].startswith("Unnamed_Device_") and device_id and existing["id"] != display_device_id:
                     self.devices[found_ip]["id"] = display_device_id
                     logger.info(f"Updated generic device ID for {found_ip} to: '{display_device_id}'")
                elif existing["id"] != display_device_id and device_id:
                    self.devices[found_ip]["id"] = display_device_id
                    logger.info(f"Updated device ID for {found_ip} to: '{display_device_id}'")

        logger.debug(f"Devices dict after update_devices: {self.devices}")
        asyncio.run_coroutine_threadsafe(self.push_devices_snapshot(), self.loop)

    def set_device_status(self, ip: str, status: str):
        logger.debug(f"set_device_status called for IP: {ip}, Status: {status}")
        with self.devices_lock:
            if ip in self.devices:
                self.devices[ip]["status"] = status
                logger.info(f"Updated status for {ip}: {self.devices[ip]}")
            else:
                self.devices[ip] = {"id": f"Unnamed_Device_{ip.replace('.', '_')}", "status": status} 
                logger.info(f"Added device with status '{status}', as it was not in list: {ip} -> {self.devices[ip]}")
        asyncio.run_coroutine_threadsafe(self.push_devices_snapshot(), self.loop)

    async def push_devices_snapshot(self):
        with self.devices_lock:
            snap = [{"ip": ip, "id": info["id"], "status": info["status"]} for ip, info in self.devices.items()]
        
        logger.debug(f"push_devices_snapshot preparing to send: {snap}")
        msg = json.dumps({"type": "devices", "data": snap})
        dead = []
        for ws in list(self.dev_ws):
            try:
                await ws.send(msg)
                logger.debug(f"Sent device snapshot to WS client {ws.remote_address}")
            except Exception as e:
                logger.error(f"Error sending device snapshot to WS client {ws.remote_address}: {e}. Marking as dead.")
                dead.append(ws)
        for d in dead:
            self.dev_ws.discard(d)

    async def broadcast_log(self, line: str):
        msg = json.dumps({"type": "log", "data": line})
        dead = []
        for ws in list(self.log_ws):
            try:
                await ws.send(msg)
            except Exception:
                logger.error(f"Error sending log to WS client. Marking as dead.")
                dead.append(ws)
        for d in dead:
            self.log_ws.discard(d)

    async def handle_command_response(self, ip: str, header_payload: dict, actual_data_bytes: bytes):
        response_info = f"CMD_RESP from {ip} - FN:{header_payload.get('fn', 'N/A')}, STAGE:{header_payload.get('stage', 'N/A')}"
        if actual_data_bytes:
            try:
                data_preview = actual_data_bytes.decode('utf-8', errors='ignore').strip()
                if len(data_preview) > 50:
                    data_preview = data_preview[:50] + "..."
                response_info += f", DATA: '{data_preview}'"
            except:
                response_info += f", DATA_HEX: {actual_data_bytes.hex()}"
        else:
            response_info += ", DATA: (empty)"

        logger.info(f"Received command response: {response_info}")
        await self.broadcast_log(response_info)

    async def ws_handler(self, websocket, path):
        try:
            logger.debug(f"WebSocket connection opened. Remote Address: {websocket.remote_address}, Path: {path}")
            try:
                msg = await asyncio.wait_for(websocket.recv(), timeout=2.0)
                reg = json.loads(msg) if msg else {}
            except Exception as e:
                logger.error(f"Error during WebSocket registration for {websocket.remote_address}: {e}")
                reg = {}
            typ = reg.get("type")
            if typ == "log":
                self.log_ws.add(websocket)
                await websocket.send(json.dumps({"type":"info","data":"log_ws_ok"}))
                try:
                    async for _ in websocket: # Keep connection open
                        pass
                except websockets.ConnectionClosed:
                    logger.info(f"Log WebSocket {websocket.remote_address} closed.")
                except Exception as e:
                    logger.error(f"Error in log WebSocket loop {websocket.remote_address}: {e}")
                finally:
                    self.log_ws.discard(websocket)
            elif typ == "devices":
                self.dev_ws.add(websocket)
                with self.devices_lock:
                    snap = [{"ip": ip, "id": info["id"], "status": info["status"]} for ip, info in self.devices.items()]
                await websocket.send(json.dumps({"type":"devices","data":snap}))
                try:
                    async for _ in websocket: # Keep connection open
                        pass
                except websockets.ConnectionClosed:
                    logger.info(f"Device WebSocket {websocket.remote_address} closed.")
                except Exception as e:
                    logger.error(f"Error in device WebSocket loop {websocket.remote_address}: {e}")
                finally:
                    self.dev_ws.discard(websocket)
            else:
                logger.warning(f"Unknown WebSocket registration type: {typ} from {websocket.remote_address}")
                await websocket.send(json.dumps({"type":"info","data":"unknown registration"}))
                await websocket.close()
        except websockets.ConnectionClosed:
            logger.info(f"WebSocket {websocket.remote_address} initially closed.")
        except Exception:
            logger.exception("An unhandled exception occurred in ws_handler.")

    async def _perform_timed_device_scan(self, duration_seconds: float):
        """
        Performs a device scan for a specified duration and then stops the temporary scanner.
        This runs in the main asyncio loop and is non-blocking.
        Only one timed scan can be active at a time. If a new one is started, the previous one is cancelled.
        """
        logger.info(f"Initiating a timed device scan for {duration_seconds} seconds.")

        # Cancel any previous active timed scan
        if self.active_timed_scan_task and not self.active_timed_scan_task.done():
            logger.info("Cancelling previous active timed scan task.")
            self.active_timed_scan_task.cancel()
            try:
                await self.active_timed_scan_task # Wait for it to finish cancelling
            except asyncio.CancelledError:
                pass # Expected
            if self.active_timed_scanner:
                self.active_timed_scanner.stop_server()
                self.active_timed_scanner.join(timeout=0.5) # Give it a moment to clean up
            self.active_timed_scanner = None
            self.active_timed_scan_task = None

        temp_scanner = UdpDiscoveryServer(on_device_found=self.update_devices)
        temp_scanner.daemon = True 
        self.active_timed_scanner = temp_scanner # Store reference to the current timed scanner

        try:
            self.active_timed_scanner.start_server() 
            current_task = asyncio.current_task()
            self.active_timed_scan_task = current_task # Store reference to this task

            await asyncio.sleep(duration_seconds)
            logger.info(f"Timed device scan completed after {duration_seconds} seconds.")
        except asyncio.CancelledError:
            logger.info(f"Timed scan task was cancelled.")
        except Exception as e:
            logger.error(f"Error during timed device scan: {e}")
        finally:
            logger.info(f"Ensuring temporary device scanner is stopped.")
            # Only stop if it's still the one we started (might have been replaced by a newer timed scan)
            if self.active_timed_scanner and self.active_timed_scanner == temp_scanner: 
                self.active_timed_scanner.stop_server()
                self.active_timed_scanner.join(timeout=0.5)
                self.active_timed_scanner = None
            if self.active_timed_scan_task == asyncio.current_task(): # Clear reference only if this was the task we started
                self.active_timed_scan_task = None

class ApiHandler(SimpleHTTPRequestHandler):
    def __init__(self, *args, directory=None, state: ServerState = None, **kwargs):
        self.server_state = state
        super().__init__(*args, directory=directory, **kwargs)

    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path.startswith("/api/"):
            self.handle_api_get(parsed.path)
            return
        if parsed.path == '/':
            self.path = '/index.html'
        try:
            super().do_GET()
        except Exception as e:
            self.send_error(404, f"File not found: {self.path}. Error: {e}")
            logger.error(f"HTTP GET error for path {self.path}: {e}")

    def do_POST(self):
        parsed = urlparse(self.path)
        if parsed.path.startswith("/api/"):
            self.handle_api_post(parsed.path)
            return
        self.send_response(404)
        self.end_headers()
        self.wfile.write(b"Not found")
        logger.warning(f"HTTP POST to unknown path: {self.path}")

    def handle_api_get(self, path):
        if path == "/api/devices":
            with self.server_state.devices_lock:
                snap = [{"ip": ip, "id": info["id"], "status": info["status"]} for ip, info in self.server_state.devices.items()]
            data = json.dumps({"status": "ok", "devices": snap}).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            logger.debug(f"Served /api/devices with {len(snap)} devices.")
            return
        elif path == "/api/scanner_status":
            status = "running" if self.server_state.continuous_discovery_server and self.server_state.continuous_discovery_server.is_alive() else "stopped"
            data = json.dumps({"status": "ok", "scanner_status": status}).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            logger.debug(f"Served /api/scanner_status with status: {status}")
            return
        elif path == "/api/log_server_status":
            status = "running" if self.server_state.log_server and self.server_state.log_server.running else "stopped"
            data = json.dumps({"status": "ok", "log_server_status": status}).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            logger.debug(f"Served /api/log_server_status with status: {status}")
            return
        self.send_response(404)
        self.end_headers()
        self.wfile.write(b"Not found")
        logger.warning(f"API GET to unknown path: {path}")

    def handle_api_post(self, path):
        length = int(self.headers.get('Content-Length', 0))
        body = self.rfile.read(length) if length > 0 else b""
        try:
            data = json.loads(body.decode("utf-8")) if body else {}
        except Exception:
            data = {}
            logger.warning(f"Failed to parse JSON body for {path}. Body: {body.decode('utf-8', errors='ignore')}")

        try:
            if path == "/api/scanner/start":
                if self.server_state.continuous_discovery_server and self.server_state.continuous_discovery_server.is_alive():
                    self._send_json(200, {"status": "scanner_already_running"})
                    logger.info("Continuous scanner start API called, but already running.")
                    return
                # Create a new instance for continuous running
                self.server_state.continuous_discovery_server = UdpDiscoveryServer(on_device_found=self.server_state.update_devices)
                self.server_state.continuous_discovery_server.daemon = True # Mark as daemon for automatic cleanup
                self.server_state.continuous_discovery_server.start_server() # Use the new start_server method
                self._send_json(200, {"status": "scanner_started"})
                logger.info("Continuous Discovery server started.")
                return
            
            elif path == "/api/scanner/stop":
                if not (self.server_state.continuous_discovery_server and self.server_state.continuous_discovery_server.is_alive()):
                    self._send_json(200, {"status": "scanner_not_running"})
                    logger.info("Continuous scanner stop API called, but not running.")
                    return
                self.server_state.continuous_discovery_server.stop_server() # Use the new stop_server method
                self.server_state.continuous_discovery_server = None # Clear reference after stopping
                self._send_json(200, {"status": "scanner_stopped"})
                logger.info("Continuous Discovery server stopped.")
                return

            elif path == "/api/scan": # "Refresh Devices" - triggers a 5-second scan
                # This initiates a *separate* 5-second scan, regardless of continuous scanner status.
                # The _perform_timed_device_scan will handle cancelling any previous timed scan.
                asyncio.run_coroutine_threadsafe(
                    self.server_state._perform_timed_device_scan(5.0), 
                    self.server_state.loop
                )
                self._send_json(200, {"status": "timed_scan_initiated"})
                logger.info("Timed device scan (5s) initiated via /api/scan.")
                return

            elif path == "/api/connect":
                ip = data.get("ip")
                if not ip:
                    self._send_json(400, {"status":"error","reason":"no ip"})
                    logger.warning("Connect API called without IP.")
                    return
                ok = self.server_state.connector.connect(ip) 
                if ok:
                    self.server_state.set_device_status(ip, "Connected")
                    self._send_json(200, {"status":"connected"})
                    logger.info(f"Connected to device {ip}.")
                else:
                    self.server_state.set_device_status(ip, "Available")
                    self._send_json(500, {"status":"error"})
                    logger.error(f"Failed to connect to device {ip}.")
                return

            elif path == "/api/disconnect":
                ip = data.get("ip")
                if not ip:
                    self._send_json(400, {"status":"error","reason":"no ip"})
                    logger.warning("Disconnect API called without IP.")
                    return
                
                try:
                    future = asyncio.run_coroutine_threadsafe(self.server_state.connector.disconnect(ip), self.server_state.loop)
                    future.result(timeout=5.0)
                    self.server_state.set_device_status(ip, "Available")
                    self._send_json(200, {"status":"disconnected"})
                    logger.info(f"Disconnected from device {ip}.")
                except asyncio.TimeoutError:
                    logger.error(f"Timeout waiting for disconnect of {ip} to complete.")
                    self._send_json(500, {"status":"error", "reason": "disconnect timeout"})
                except Exception as e:
                    logger.error(f"Error during disconnect for {ip}: {e}")
                    self._send_json(500, {"status":"error", "reason": str(e)})
                return

            elif path == "/api/send":
                ip = data.get("ip")
                if not ip:
                    self._send_json(400, {"status":"error","reason":"no ip"})
                    logger.warning("Send API called without IP.")
                    return
                payload = None
                if all(k in data for k in ("fn","stage","data_base64")):
                    b = base64_to_bytes(data["data_base64"])
                    payload = encode_request(int(data["fn"]), int(data["stage"]), b)
                    logger.debug(f"Sending protocol payload (fn={data['fn']}, stage={data['stage']}) to {ip}. Data: '{data['data_base64'][:50]}...'")
                else:
                    self._send_json(400, {"status":"error","reason":"no payload"})
                    logger.warning(f"Send API called for {ip} with invalid payload format.")
                    return
                
                ok = self.server_state.connector.send(ip, payload)
                self._send_json(200, {"status": "sent" if ok else "error"})
                if ok:
                    logger.info(f"Payload sent successfully to {ip}.")
                else:
                    logger.error(f"Failed to send payload to {ip}.")
                return

            elif path == "/api/log/start":
                if self.server_state.log_server and self.server_state.log_server.running:
                    self._send_json(200, {"status":"already_running"})
                    logger.info("Log server start API called, but already running.")
                    return
                def on_receive(line):
                    asyncio.run_coroutine_threadsafe(self.server_state.broadcast_log(line), self.server_state.loop)
                self.server_state.log_server = UdpLogServer(on_receive=on_receive)
                self.server_state.log_server.start()
                self._send_json(200, {"status":"started"})
                logger.info("Log server started.")
                return

            elif path == "/api/log/stop":
                if not self.server_state.log_server or not self.server_state.log_server.running:
                    self._send_json(200, {"status":"not_running"})
                    logger.info("Log server stop API called, but not running.")
                    return
                self.server_state.log_server.stop()
                self.server_state.log_server.join(timeout=1.0)
                self.server_state.log_server = None
                self._send_json(200, {"status":"stopped"})
                logger.info("Log server stopped.")
                return

            else:
                self._send_json(404, {"status":"not_found"})
                logger.warning(f"API POST to unknown path: {path}")
        except Exception as e:
            logger.exception(f"An unhandled exception occurred in handle_api_post for path {path}.")
            self._send_json(500, {"status":"error", "reason": str(e)})

    def _send_json(self, code: int, obj):
        data = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

def run_http_server(state: ServerState, static_dir: str, port: int = HTTP_PORT):
    handler = lambda *args, **kwargs: ApiHandler(*args, directory=static_dir, state=state, **kwargs)
    server = HTTPServer(("", port), handler)
    logger.info(f"HTTP server serving static + API at http://0.0.0.0:{port}")
    server.serve_forever()

async def main_startup_logic(): # Renamed to clearly indicate this is the primary startup
    global main_loop_state_instance # Access the global instance
    
    loop = asyncio.get_event_loop()
    main_loop_state_instance = ServerState(loop) # Create ONE ServerState instance
    static_dir = os.path.join(os.path.dirname(__file__), "static")
    if not os.path.isdir(static_dir):
        logger.error(f"Static directory missing: {static_dir}")
        return

    t = threading.Thread(target=run_http_server, args=(main_loop_state_instance, static_dir, HTTP_PORT), daemon=True)
    t.start()

    logger.info(f"WebSocket server starting on 0.0.0.0:{WS_PORT}")
    start_server = websockets.serve(main_loop_state_instance.ws_handler, "", WS_PORT)

    # === 自动启动 Log 服务器 ===
    def on_receive_log_line(line):
        asyncio.run_coroutine_threadsafe(main_loop_state_instance.broadcast_log(line), main_loop_state_instance.loop)
    main_loop_state_instance.log_server = UdpLogServer(on_receive=on_receive_log_line)
    main_loop_state_instance.log_server.start()
    logger.info("Log Server automatically started in the background.")
    # ==========================

    # === 工具启动时, 自动进行一次5秒的设备刷新 (Timed Scan) ===
    asyncio.create_task(main_loop_state_instance._perform_timed_device_scan(5.0))
    logger.info("Initial 5-second device discovery initiated.")
    # ======================================================

    # === 自动启动 FTP 服务器 ===
    main_loop_state_instance.ftp_server = ProbeFtpServer(host="0.0.0.0", port=2121, username="user", password="12345") # Create ONE FTP Server instance
    main_loop_state_instance.ftp_server.start() # Start it ONE time
    logger.info("FTP Server automatically started in the background.")
    # ==========================

    async with start_server:
        logger.info(f"All background services initialized. HTTP on {HTTP_PORT}, WS on {WS_PORT}. Awaiting shutdown signal.")
        await asyncio.Future() # Keep the main loop running indefinitely

if __name__ == "__main__":
    logging.basicConfig(
        level=logging.DEBUG, 
        format='%(asctime)s - %(filename)s:%(lineno)d - %(levelname)s - %(message)s',
        datefmt='%Y-%m-%d %H:%M:%S'
    )

    logger.info("Starting probe web service...")
    main_loop_state_instance: Optional[ServerState] = None # Declare global here
    try:
        asyncio.run(main_startup_logic()) # Call the single startup function
    except KeyboardInterrupt:
        logger.info("Shutting down due to KeyboardInterrupt...")
    finally:
        # Cleanup: Stop the continuous discovery server if it's active
        if main_loop_state_instance and main_loop_state_instance.continuous_discovery_server:
            logger.info("Stopping continuous discovery server during shutdown.")
            main_loop_state_instance.continuous_discovery_server.stop_server()
            # As it's a daemon thread and main process is exiting, explicit join might not be strictly needed,
            # but stop_server does attempt a join for graceful exit.
        
        # Cleanup: Stop the active timed scanner if it's running
        if main_loop_state_instance and main_loop_state_instance.active_timed_scanner:
            logger.info("Stopping active timed scanner during shutdown.")
            main_loop_state_instance.active_timed_scanner.stop_server()
            if main_loop_state_instance.active_timed_scan_task and not main_loop_state_instance.active_timed_scan_task.done():
                main_loop_state_instance.active_timed_scan_task.cancel()
                try:
                    # Attempt to get the event loop to run the cancellation, but it might already be closed.
                    # For a robust shutdown, this should ideally be handled within the main_startup_logic.
                    # As a fallback in finally, we try to get a loop and run.
                    cleanup_loop = None
                    try:
                        cleanup_loop = asyncio.get_event_loop()
                    except RuntimeError:
                        cleanup_loop = asyncio.new_event_loop()
                        asyncio.set_event_loop(cleanup_loop) # Set for this thread only if new

                    if cleanup_loop and not cleanup_loop.is_closed():
                        cleanup_loop.run_until_complete(main_loop_state_instance.active_timed_scan_task)
                except (asyncio.CancelledError, RuntimeError): # RuntimeError if loop already closed/not running
                    pass
                except Exception as e:
                    logger.warning(f"Error cancelling active_timed_scan_task during shutdown: {e}")
            
        # Cleanup logic remains the same for other services, operating on the single main_loop_state_instance
        if main_loop_state_instance and main_loop_state_instance.log_server:
            main_loop_state_instance.log_server.stop()
            main_loop_state_instance.log_server.join(timeout=1.0)
            logger.info("Log server stopped during shutdown.")
        # === 确保 FTP 服务器在关闭时停止 ===
        if main_loop_state_instance and main_loop_state_instance.ftp_server:
            logger.info("Attempting to stop FTP server during shutdown.")
            if hasattr(main_loop_state_instance.ftp_server, 'stop') and callable(main_loop_state_instance.ftp_server.stop):
                main_loop_state_instance.ftp_server.stop() 
                main_loop_state_instance.ftp_server.join(timeout=3.0) 
                logger.info("FTP server stopped during shutdown.")
            else:
                logger.warning(f"FTP server object during shutdown lacks callable 'stop' method. Type: {type(main_loop_state_instance.ftp_server)}")
        # ===================================
        if main_loop_state_instance and main_loop_state_instance.connector:
            current_loop = None
            try:
                current_loop = asyncio.get_event_loop()
            except RuntimeError:
                logger.warning("No running event loop found for final connector cleanup. Creating a new one.")
                current_loop = asyncio.new_event_loop()
                asyncio.set_event_loop(current_loop) # Set it for this thread

            disconnect_tasks = []
            for ip in list(main_loop_state_instance.connector.connections.keys()): 
                logger.info(f"Scheduling disconnect for {ip} during shutdown.")
                try:
                    disconnect_tasks.append(main_loop_state_instance.connector.disconnect(ip))
                except Exception as e:
                    logger.warning(f"Could not schedule disconnect coroutine for {ip} during shutdown: {e}")
            
            if disconnect_tasks and current_loop:
                async def run_disconnects():
                    await asyncio.gather(*disconnect_tasks, return_exceptions=True)
                
                try:
                    current_loop.run_until_complete(run_disconnects())
                except Exception as e:
                    logger.warning(f"Error during final connector disconnects: {e}")
                finally:
                    if not current_loop.is_closed():
                        current_loop.close()
            logger.info("All active device connections cleanup initiated during shutdown.")
