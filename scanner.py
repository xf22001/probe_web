# scanner.py
import socket
import threading
import time
from typing import Dict, Any, Callable, Optional
import logging

logger = logging.getLogger(__name__)

BROADCAST_PORT = 6000

class UdpDiscoveryServer(threading.Thread):
    def __init__(self, on_device_found: Callable[[str, str], None], port: int = BROADCAST_PORT):
        super().__init__()
        self.on_device_found = on_device_found
        self.port = port
        self.running = False
        self.sock: Optional[socket.socket] = None
        
    def run(self):
        self.running = True # 在这里设置运行标志
        try:
            self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1) 
            # 启用广播模式，以便能够接收来自任何源的广播消息
            # 在某些系统上，可能需要明确设置 SO_BROADCAST 才能接收广播
            # self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1) 
            self.sock.bind(("", self.port))
            self.sock.settimeout(1.0) # 设置超时，以便循环可以检查self.running标志
            logger.info(f"UDP Discovery Server listening for client probes on port {self.port}")

            while self.running:
                data, addr = None, None # Initialize to None for error reporting
                try:
                    data, addr = self.sock.recvfrom(1024) # 缓冲区大小
                    ip_address = addr[0]
                    
                    device_id_bytes = data
                    # 查找第一个 null 终止符 (0x00)
                    null_idx = device_id_bytes.find(b'\x00')
                    if null_idx != -1:
                        # 如果找到 null 终止符，只取它之前的部分
                        device_id_bytes = device_id_bytes[:null_idx]
                        logger.debug(f"Found null terminator at index {null_idx}. Truncating ID bytes.")
                    
                    # 解码为字符串，并移除首尾空白（虽然 C 字符串通常不带）
                    response_str = device_id_bytes.decode('utf-8', errors='ignore').strip()
                    device_id = ""

                    if response_str: # 如果解码后的字符串不为空
                        device_id = response_str
                        logger.debug(f"Decoded non-empty ID from bytes: '{device_id}'")
                    else:
                        # 如果解码后仍为空（例如客户端只发送了0x00），则分配一个默认 ID
                        device_id = f"Unnamed_Device_{ip_address.replace('.', '_')}"
                        logger.warning(f"Received empty or null-only probe message from {ip_address}. Assigning default ID: {device_id}")
                    
                    if device_id: 
                        logger.debug(f"Calling on_device_found for IP={ip_address}, ID='{device_id}'")
                        self.on_device_found(ip_address, device_id)
                    else:
                        logger.warning(f"Skipping device discovery for {ip_address}: No valid ID could be determined from raw data {data.hex()}.")

                except socket.timeout:
                    pass # 超时是预期行为，用于检查self.running
                except OSError as e: # Catch OSError specifically for closed socket
                    if not self.running: # If server is stopping, this is expected
                        logger.info(f"UDP discovery server socket error during shutdown: {e}")
                    else:
                        logger.exception(f"Unexpected OSError in UDP Discovery Server from {addr[0] if addr else 'N/A'}.")
                    break # Break the loop on socket errors
                except Exception as e:
                    log_ip = addr[0] if addr else 'N/A'
                    logger.exception(f"Error in UDP Discovery Server processing data from {log_ip}: {e}")
                    
        finally:
            self.stop() # 确保在线程结束时调用stop进行清理

    def start(self):
        """启动扫描器线程。"""
        if self.running:
            logger.info("Discovery server is already running. No action taken by start().")
            return
        
        super().start() # 调用threading.Thread的start方法，在单独线程中运行self.run()
        logger.info("UDP Discovery Server starting...")

    def stop(self):
        """停止扫描器线程和关闭资源。"""
        if self.running: # 只有当它正在运行时才执行停止逻辑
            self.running = False # 设置标志，通知run方法退出循环
            if self.sock:
                try:
                    self.sock.close() # 关闭socket，解除阻塞的recvfrom
                    self.sock = None
                    logger.debug("Discovery server socket closed.")
                except Exception as e:
                    logger.error(f"Error closing discovery socket during stop: {e}")
            logger.info("UDP Discovery Server stopped.")
        else:
            logger.info("Discovery server is not running, no stop action needed.")
