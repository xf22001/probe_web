# ./scanner.py
import socket
import threading
import time
from typing import Callable, Optional
import logging

logger = logging.getLogger(__name__)

BROADCAST_PORT = 6000

class UdpDiscoveryServer(threading.Thread):
    def __init__(self, on_device_found: Callable[[str, str], None], port: int = BROADCAST_PORT):
        super().__init__()
        self.on_device_found = on_device_found
        self.port = port
        self.running = False # Overall status flag
        self._stop_event = threading.Event() # For signaling the thread to stop
        self.sock: Optional[socket.socket] = None
        
    def run(self):
        self.running = True
        logger.info(f"UDP Discovery Server thread starting, listening on port {self.port}")
        try:
            self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1) 
            self.sock.bind(("", self.port))
            self.sock.settimeout(1.0) # Timeout for non-blocking check of _stop_event

            while not self._stop_event.is_set(): # Use stop_event for explicit stops
                try:
                    data, addr = self.sock.recvfrom(1024)
                    ip_address = addr[0]
                    
                    device_id_bytes = data
                    # 查找第一个 null 终止符 (0x00)
                    null_idx = device_id_bytes.find(b'\x00')
                    if null_idx != -1:
                        # 如果找到 null 终止符，只取它之前的部分
                        device_id_bytes = device_id_bytes[:null_idx]
                    
                    # 解码为字符串，并移除首尾空白（虽然 C 字符串通常不带）
                    response_str = device_id_bytes.decode('utf-8', errors='ignore').strip()
                    device_id = response_str if response_str else f"Unnamed_Device_{ip_address.replace('.', '_')}"
                    
                    if device_id: 
                        self.on_device_found(ip_address, device_id)
                    else:
                        logger.warning(f"Skipping device discovery for {ip_address}: No valid ID could be determined from raw data {data.hex()}.")

                except socket.timeout:
                    pass
                except Exception as e:
                    logger.exception(f"Error in UDP Discovery Server for {ip_address}.")
                    # If a critical error, stop the server
                    self._stop_event.set()
        finally:
            self._cleanup()
            logger.info("UDP Discovery Server thread stopped.")

    def _cleanup(self):
        """Internal cleanup for the socket."""
        if self.sock:
            self.sock.close()
            self.sock = None
            logger.debug("Discovery server socket closed.")
        self.running = False # Set running to false during cleanup

    def start_server(self): # Renamed to avoid confusion with threading.Thread.start
        """Starts the UDP Discovery Server thread."""
        if self.is_alive(): # Checks if the thread is already running
            logger.info("Discovery server thread is already alive.")
            return

        self._stop_event.clear() # Clear any previous stop requests
        # self.running is set to True in run() method
        super().start() # Calls threading.Thread.start(), which calls self.run()
        logger.info("UDP Discovery Server starting...")

    def stop_server(self): # Renamed to avoid confusion with threading.Thread.stop
        """Stops the UDP Discovery Server thread and waits for it to finish."""
        if self.is_alive():
            logger.info("Signaling UDP Discovery Server to stop.")
            self._stop_event.set() # Signal the thread to stop
            self.join(timeout=1.0) # Wait for the thread to finish its run loop and cleanup
            if self.is_alive():
                logger.warning("UDP Discovery Server thread did not terminate gracefully within 1 second.")
