# connector.py
import socket
import threading
import time
from typing import Dict, Any, Callable, Optional
import logging
import asyncio 

from protocol import decode_request, encode_request

# 获取当前模块的日志器
logger = logging.getLogger(__name__)

PROBE_TOOL_PORT = 6001 # 客户端监听命令的端口

class Connector:
    def __init__(self, loop: asyncio.AbstractEventLoop, on_command_response: Optional[Callable[[str, dict, bytes], None]] = None):
        self.loop = loop # 存储事件循环
        # connections: Dict[ip, {"sock": socket_obj, "listener_thread": thread_obj, "stop_event": threading.Event, "keep_alive_task": asyncio.Task, "local_ip": str}]
        self.connections: Dict[str, Dict[str, Any]] = {}
        self.on_command_response = on_command_response
        self.lock = threading.Lock()

    async def _send_keep_alive(self, ip: str):
        """
        异步任务：每秒向指定IP发送保活消息。
        """
        logger.debug(f"Starting keep-alive for {ip}")
        # fn=0xffffffff (对应C++中的unsigned int -1)
        # stage=0
        # data=b'\x00' (单个空字节，与C++示例一致)
        keep_alive_payload = encode_request(fn=0xFFFFFFFF, stage=0, data=b'\x00')
        
        while True:
            try:
                # 检查连接是否仍然存在，并加锁访问socket
                with self.lock:
                    sock_info = self.connections.get(ip)
                    # Only send if the socket is still open, exists in connections, and not explicitly stopping
                    if sock_info and sock_info.get("sock") and not sock_info["stop_event"].is_set():
                        sock_info["sock"].send(keep_alive_payload)
                        logger.debug(f"Sent keep-alive to {ip}")
                    else:
                        logger.info(f"Keep-alive for {ip} stopping: socket not found, closed, or stop event set.")
                        break # If socket is gone or stop event is set, exit loop
            except asyncio.CancelledError:
                logger.info(f"Keep-alive task for {ip} was cancelled.")
                break # Exit gracefully if cancelled
            except Exception as e:
                logger.error(f"Error sending keep-alive to {ip}: {e}. Disconnecting.")
                # If send fails, attempt to disconnect and clean up
                # Using call_soon_threadsafe to schedule an async task on the main event loop
                self.loop.call_soon_threadsafe(
                    lambda ip_arg=ip: asyncio.create_task(self.disconnect(ip_arg)), 
                    ip 
                )
                break
            await asyncio.sleep(1) # Send once per second

    def _response_listener_thread(self, ip: str, sock: socket.socket, stop_event: threading.Event):
        """
        同步线程：监听UDP响应，并将回调调度到主事件循环。
        """
        logger.info(f"Starting UDP response listener thread for {ip} on socket {sock.fileno()}")
        
        try:
            while not stop_event.is_set():
                try:
                    data, sender_addr = sock.recvfrom(4096)
                    
                    if sender_addr[0] == ip: # Ensure we only process responses from the target IP
                        header_payload, actual_data = decode_request(data)
                        if header_payload and self.on_command_response:
                            # Schedule the async callback to run in the main event loop
                            asyncio.run_coroutine_threadsafe(
                                self.on_command_response(ip, header_payload, actual_data), 
                                self.loop
                            )
                        else:
                            logger.debug(f"Failed to decode response from {ip} or no callback. Raw: {data.hex()}")
                    else:
                        logger.debug(f"Ignored UDP packet from {sender_addr[0]} (not target IP {ip}). Raw: {data.hex()}")
                except socket.timeout:
                    pass # Timeout is expected for checking stop_event
                except OSError as e: # Catch OSError specifically for closed socket
                    # Errno 9 (Bad file descriptor) is common when a socket is closed from another thread
                    if stop_event.is_set() and e.errno == 9: 
                        logger.info(f"UDP response listener for {ip} detected socket closed (Errno 9) due to stop event. Exiting.")
                    else:
                        logger.exception(f"Unexpected OSError in UDP response listener for {ip}.")
                    break # Break the loop on socket errors
                except Exception as e:
                    logger.exception(f"Error in UDP response listener for {ip}.")
                    # If listener thread fails unexpectedly, trigger disconnect
                    self.loop.call_soon_threadsafe(
                        lambda ip_arg=ip: asyncio.create_task(self.disconnect(ip_arg)),
                        ip
                    )
                    break
        finally:
            logger.info(f"UDP response listener thread for {ip} stopping cleanly.")

    def connect(self, ip: str, port: int = PROBE_TOOL_PORT) -> bool:
        """
        Creates a UDP "connection" context with a listener thread and keep-alive task for the device.
        """
        with self.lock:
            if ip in self.connections and self.connections[ip].get("sock"):
                logger.info(f"UDP 'connection' context for {ip} already exists and is active.")
                return True

            sock = None
            try:
                sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                sock.settimeout(0.1) # Short timeout to make recvfrom interruptible

                # UDP's connect() just sets the default target IP and port; it doesn't establish a TCP-like connection.
                # However, it allows us to use getsockname() to find out which local interface is being used.
                sock.connect((ip, port)) 
                
                # Capture the local IP address used for this connection
                local_ip = sock.getsockname()[0]
                logger.info(f"Socket for target {ip} bound to local IP: {local_ip}")

                stop_event = threading.Event()
                listener_thread = threading.Thread(
                    target=self._response_listener_thread, 
                    args=(ip, sock, stop_event), 
                    daemon=True
                )
                listener_thread.start()

                # Create and start the asynchronous keep-alive task
                keep_alive_task = self.loop.create_task(self._send_keep_alive(ip))
                
                self.connections[ip] = {
                    "sock": sock,
                    "listener_thread": listener_thread,
                    "stop_event": stop_event,
                    "keep_alive_task": keep_alive_task,
                    "local_ip": local_ip 
                }

                logger.info(f"UDP 'connection' context created and listener/keep-alive started for {ip}:{port}")
                
                return True
            except Exception as e:
                logger.error(f"Failed to create UDP 'connection' context for {ip}:{port}: {e}")
                # Clean up any partially created state
                if sock:
                    sock.close()
                # Ensure keep-alive task is cancelled if it was created before the error
                if 'keep_alive_task' in locals() and keep_alive_task and not keep_alive_task.done():
                    keep_alive_task.cancel()
                return False

    def get_local_ip(self, ip: str) -> Optional[str]:
        """Returns the local IP address being used to communicate with the target IP."""
        with self.lock:
            if ip in self.connections:
                return self.connections[ip].get("local_ip")
            return None

    async def disconnect(self, ip: str) -> bool:
        """
        Disconnects the UDP "connection" context for a device, stopping the listener thread and keep-alive task.
        """
        with self.lock:
            if ip not in self.connections:
                logger.info(f"No UDP 'connection' context for {ip} to disconnect.")
                return False
            
            # Remove the entry immediately
            conn_info = self.connections.pop(ip) 
            
            # 1. Signal listener thread to stop
            if conn_info.get("stop_event"):
                conn_info["stop_event"].set()
            
            # 2. Close the socket
            if conn_info.get("sock"):
                try:
                    conn_info["sock"].close()
                    logger.info(f"Closed UDP socket for {ip}.")
                except Exception as e:
                    logger.warning(f"Error closing UDP socket for {ip}: {e}")
            
            # 3. Cancel keep-alive task
            if conn_info.get("keep_alive_task"):
                conn_info["keep_alive_task"].cancel()
                try:
                    await conn_info["keep_alive_task"] 
                    logger.debug(f"Keep-alive task for {ip} cancelled successfully.")
                except asyncio.CancelledError:
                    pass 
                except Exception as e:
                    logger.warning(f"Error awaiting cancelled keep-alive task for {ip}: {e}")
            
            logger.info(f"UDP 'disconnected' context for {ip} cleaned up.")
            return True

    def send(self, ip: str, payload_bytes: bytes) -> bool:
        """
        Sends UDP data to the specified IP.
        """
        with self.lock: 
            sock_info = self.connections.get(ip)
            if not sock_info or not sock_info.get("sock") or sock_info["stop_event"].is_set():
                logger.warning(f"Cannot send, no active UDP 'connection' context for {ip} or stop event set.")
                return False

            try:
                sock_info["sock"].send(payload_bytes)
                logger.debug(f"Sent {len(payload_bytes)} bytes to {ip}:{PROBE_TOOL_PORT}")
                return True
            except Exception as e:
                logger.error(f"Error sending UDP data to {ip}: {e}")
                self.loop.call_soon_threadsafe(
                    lambda ip_arg=ip: asyncio.create_task(self.disconnect(ip_arg)), 
                    ip 
                )
                return False
