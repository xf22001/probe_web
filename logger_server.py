# logger_server.py
import socket
import threading
import time
import datetime
import os
from typing import Callable, Optional
import logging # 导入 logging

# 获取当前模块的日志器
logger = logging.getLogger(__name__)

# 更新端口为 probe_tool.h 中的 LOG_TOOL_PORT (同步为6002)
LOG_TOOL_PORT = 6002 

class UdpLogServer(threading.Thread):
    def __init__(self, on_receive: Callable[[str], None], log_dir: str = "logs"):
        super().__init__()
        self.on_receive = on_receive
        self.log_dir = log_dir
        self.running = False
        self.sock: Optional[socket.socket] = None
        self.log_file_handle: Optional[object] = None 
        self.log_file_path: Optional[str] = None
        
        if not os.path.exists(self.log_dir):
            os.makedirs(self.log_dir)
            logger.info(f"Created log directory: {self.log_dir}")

    def _open_log_file(self):
        """打开一个新的日志文件，如果已存在则关闭旧的。"""
        if self.log_file_handle:
            try:
                self.log_file_handle.close()
                self.log_file_handle = None
                logger.debug(f"Closed previous log file: {self.log_file_path}")
            except Exception as e:
                logger.error(f"Error closing previous log file {self.log_file_path}: {e}")

        timestamp = datetime.datetime.now().strftime("%Y%m%d_%H%M%S")
        self.log_file_path = os.path.join(self.log_dir, f"log_{timestamp}.txt")
        try:
            self.log_file_handle = open(self.log_file_path, "a", encoding="utf-8")
            logger.info(f"Opened new log file: {self.log_file_path}")
        except Exception as e:
            logger.error(f"Error opening log file {self.log_file_path}: {e}")
            self.log_file_handle = None

    def run(self):
        """线程主函数，监听UDP端口接收日志。"""
        self.running = True # 在这里设置运行标志
        self._open_log_file() # 启动时打开日志文件

        try:
            self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1) # 允许地址重用
            # 对于 Windows 上的 UDP，可能还需要 SO_EXCLUSIVEADDRUSE 来避免某些问题，但 SO_REUSEADDR 更通用
            # self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1) # 启用广播接收，如果需要
            self.sock.bind(("", LOG_TOOL_PORT))
            self.sock.settimeout(1.0) # 设置超时，以便循环可以检查self.running标志
            logger.info(f"UDP Log Server listening on port {LOG_TOOL_PORT}")

            while self.running:
                try:
                    data, addr = self.sock.recvfrom(4096)
                    log_line = data.decode('utf-8', errors='ignore').strip()
                    
                    current_time = datetime.datetime.now().strftime("[%Y-%m-%d %H:%M:%S.%f]")[:-3]
                    # 日志格式现在包含发送者IP，以与C++工具的日志源识别保持一致
                    formatted_log = f"{current_time} [{addr[0]}:{addr[1]}] {log_line}"
                    
                    self.on_receive(formatted_log) # 调用回调函数，通常是推送到WebSocket

                    if self.log_file_handle:
                        self.log_file_handle.write(formatted_log + "\n")
                        self.log_file_handle.flush() # 立即写入磁盘
                        
                except socket.timeout:
                    pass # 超时是预期行为，用于检查self.running
                except OSError as e: # Catch OSError for socket errors, e.g., bad file descriptor when socket is closed
                    if not self.running: # If server is stopping, this is expected
                        logger.info(f"UDP log server for {self.ident} socket error during shutdown: {e}")
                    else:
                        logger.exception(f"Unexpected OSError in UDP log server: {e}")
                    break # Break the loop on socket errors
                except Exception as e:
                    logger.exception(f"Error in UDP log server: {e}")
                    # 在发生意外错误时，也可能需要停止服务
                    break 
        finally:
            self.stop() # 确保在线程结束时调用stop进行清理

    def start(self):
        """启动日志服务器线程。"""
        if self.running:
            logger.info("Log server is already running. No action taken by start().")
            return

        super().start() # 调用threading.Thread的start方法，在单独线程中运行self.run()
        logger.info("UDP Log Server starting...")
            
    def stop(self):
        """停止日志服务器线程和关闭资源。"""
        if self.running: # 只有当它正在运行时才执行停止逻辑
            self.running = False # 设置标志，通知run方法退出循环
            if self.sock:
                self.sock.close() # 关闭socket，解除阻塞的recvfrom
                self.sock = None
                logger.debug("Log server socket closed.")
            if self.log_file_handle:
                try:
                    self.log_file_handle.close() # 关闭日志文件
                    self.log_file_handle = None
                    logger.debug("Log file closed.")
                except Exception as e:
                    logger.error(f"Error closing log file during stop: {e}")
            logger.info("UDP Log Server stopped and resources released.")
        else:
            logger.info("Log server is not running, no stop action needed.")
