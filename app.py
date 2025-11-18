# app.py
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
        self.discovery_server: Optional[UdpDiscoveryServer] = None 
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
                    # 修正: found_pullid -> found_ip
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
            status = "running" if self.server_state.discovery_server and self.server_state.discovery_server.running else "stopped"
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
        except json.JSONDecodeError: # 更具体的异常类型
            data = {}
            logger.warning(f"Failed to parse JSON body for {path}. Body: {body.decode('utf-8', errors='ignore')}")
        except Exception:
            data = {}
            logger.warning(f"An unexpected error occurred parsing JSON body for {path}. Body: {body.decode('utf-8', errors='ignore')}")

        try:
            if path == "/api/scanner/start":
                if self.server_state.discovery_server and self.server_state.discovery_server.running:
                    self._send_json(200, {"status": "scanner_already_running"})
                    logger.info("Scanner start API called, but already running.")
                    return
                self.server_state.discovery_server = UdpDiscoveryServer(on_device_found=self.server_state.update_devices)
                self.server_state.discovery_server.start()
                self._send_json(200, {"status": "scanner_started"})
                logger.info("Scanner API called, Discovery server started.")
                return
            
            elif path == "/api/scanner/stop":
                if not self.server_state.discovery_server or not self.server_state.discovery_server.running:
                    self._send_json(200, {"status": "scanner_not_running"})
                    logger.info("Scanner stop API called, but not running.")
                    return
                self.server_state.discovery_server.stop()
                self.server_state.discovery_server.join(timeout=1.0)
                self.server_state.discovery_server = None
                self._send_json(200, {"status": "scanner_stopped"})
                logger.info("Discovery server stopped.")
                return

            elif path == "/api/scan":
                if not self.server_state.discovery_server or not self.server_state.discovery_server.running:
                    logger.info("Scan API called, but scanner was not running. Starting it now.")
                    self.server_state.discovery_server = UdpDiscoveryServer(on_device_found=self.server_state.update_devices)
                    self.server_state.discovery_server.start()
                    self._send_json(200, {"status": "scanner_started_and_scanned"})
                else:
                    self._send_json(200, {"status": "scanner_already_running_and_scanned"})
                logger.info("Scan API called, ensured Discovery server is active.")
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
                    # 在主事件循环中调度异步 disconnect 任务
                    # 创建任务并在事件循环中运行，不会阻塞当前线程
                    task = asyncio.run_coroutine_threadsafe(self.server_state.connector.disconnect(ip), self.server_state.loop)
                    task.result(timeout=5.0) # 等待任务完成，有超时
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

    # 启动 HTTP 服务器线程
    http_thread = threading.Thread(target=run_http_server, args=(main_loop_state_instance, static_dir, HTTP_PORT), daemon=True)
    http_thread.start()

    logger.info(f"WebSocket server starting on 0.0.0.0:{WS_PORT}")
    ws_server = websockets.serve(main_loop_state_instance.ws_handler, "", WS_PORT)

    # === 自动启动 FTP 服务器 ===
    main_loop_state_instance.ftp_server = ProbeFtpServer(host="0.0.0.0", port=2121, username="user", password="12345") # Create ONE FTP Server instance
    main_loop_state_instance.ftp_server.start() # Start it ONE time
    logger.info("FTP Server automatically started in the background.")
    # ==========================

    try:
        async with ws_server:
            logger.info(f"UDP Discovery Server is initially stopped as per requirement.")
            await asyncio.Future() # This keeps the event loop running indefinitely
    except asyncio.CancelledError:
        logger.info("Main startup logic was cancelled.")
    except KeyboardInterrupt:
        logger.info("KeyboardInterrupt caught in main_startup_logic, initiating graceful shutdown.")
    finally:
        # 在主事件循环仍然运行时进行异步清理
        logger.info("Initiating async cleanup for Connector connections...")
        if main_loop_state_instance and main_loop_state_instance.connector:
            disconnect_tasks = [main_loop_state_instance.connector.disconnect(ip) 
                                for ip in list(main_loop_state_instance.connector.connections.keys())]
            if disconnect_tasks:
                # 使用 asyncio.gather 来并行等待所有断开连接任务完成
                # return_exceptions=True 确保即使某个任务失败，也不会中断其他任务
                await asyncio.gather(*disconnect_tasks, return_exceptions=True)
                logger.info("All active device connections cleanup completed.")
            else:
                logger.info("No active device connections to cleanup.")

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
        # Cleanup logic for thread-based services (Discovery, Log, FTP)
        logger.info("Initiating synchronous cleanup for thread-based services...")
        if main_loop_state_instance and main_loop_state_instance.discovery_server:
            main_loop_state_instance.discovery_server.stop()
            main_loop_state_instance.discovery_server.join(timeout=1.0)
            logger.info("Discovery server stopped during shutdown.")
        if main_loop_state_instance and main_loop_state_instance.log_server:
            main_loop_state_instance.log_server.stop()
            main_loop_state_instance.log_server.join(timeout=1.0)
            logger.info("Log server stopped during shutdown.")
        # === 确保 FTP 服务器在关闭时停止 ===
        if main_loop_state_instance and main_loop_state_instance.ftp_server:
            logger.info("Attempting to stop FTP server during shutdown.")
            # 检查 stop 方法是否存在且可调用，以增加健壮性
            if hasattr(main_loop_state_instance.ftp_server, 'stop') and callable(main_loop_state_instance.ftp_server.stop):
                main_loop_state_instance.ftp_server.stop() 
                main_loop_state_instance.ftp_server.join(timeout=3.0) 
                logger.info("FTP server stopped during shutdown.")
            else:
                logger.warning(f"FTP server object during shutdown lacks callable 'stop' method. Type: {type(main_loop_state_instance.ftp_server)}")
        logger.info("Probe web service shutdown complete.")
