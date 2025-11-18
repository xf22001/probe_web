# ftp_server.py
import os
import threading
import asyncio
import logging
import time

from pyftpdlib.handlers import FTPHandler
from pyftpdlib.authorizers import DummyAuthorizer
from pyftpdlib.servers import FTPServer
from pyftpdlib.ioloop import IOLoop # 确保导入了 pyftpdlib 的 IOLoop

logger = logging.getLogger(__name__)

# 配置 pyftpdlib 的日志器，提供更详细的输出
logging.getLogger('pyftpdlib').setLevel(logging.INFO) # 调整为 INFO，避免过多 DEBUG
logging.getLogger('pyftpdlib.servers').setLevel(logging.INFO)
logging.getLogger('pyftpdlib.handlers').setLevel(logging.INFO)


class ThrottledFTPHandler(FTPHandler):
    # 修正: 重新添加 ioloop 参数，并传递给 super().__init__()
    # FTPServer 会在实例化 handler 时传递这个参数
    def __init__(self, conn, server, ioloop=None): # ioloop=None 作为一个安全默认值
        super().__init__(conn, server, ioloop=ioloop)
        self.read_limit = 100 * 1024  # 100 KB/s
        self.timeout = 300  # 控制连接超时，默认60s
        self.data_timeout = 300  # 数据连接超时，默认30s

    def on_incomplete_file_send(self, file):
        logger.debug(f"FTP_HANDLER: Incomplete file send: {file}")
        super().on_incomplete_file_send(file)

    def on_file_send(self, file):
        logger.debug(f"FTP_HANDLER: File sent: {file}")
        super().on_file_send(file)

    def on_login(self, username):
        super().on_login(username)
        logger.info(f"FTP_HANDLER: User '{username}' logged in from {self.remote_ip}.")

    def on_logout(self, username):
        super().on_logout(username)
        logger.info(f"FTP_HANDLER: User '{username}' logged out from {self.remote_ip}.")

    def on_disconnect(self):
        super().on_disconnect()
        logger.info(f"FTP_HANDLER: Client {self.remote_ip} disconnected.")
    
    def on_quit(self):
        super().on_quit()
        logger.info(f"FTP_HANDLER: Client {self.remote_ip} sent QUIT command.")

    def ftp_CMD(self, line):
        # logger.debug(f"FTP_HANDLER: CMD from {self.remote_ip}: {line.strip()}") # 如果需要更详细的命令日志
        super().ftp_CMD(line)
    
    def send_reply(self, ftpcodes, msg=""):
        # logger.debug(f"FTP_HANDLER: Reply to {self.remote_ip}: {ftpcodes} {msg.strip()}") # 如果需要更详细的回复日志
        super().send_reply(ftpcodes, msg)


class ProbeFtpServer(threading.Thread):
    def __init__(self, host="0.0.0.0", port=2121, username="user", password="123", anonymous_root="."):
        super().__init__()
        self.host = host
        self.port = port
        self.username = username
        self.password = password
        self.anonymous_root = anonymous_root
        self.server: Optional[FTPServer] = None
        self.running = False

        self.passive_ports_range = (50000, 50010) 
        
    def run(self):
        self.running = True
        logger.info(f"FTP_SERVER: Starting FTP server on {self.host}:{self.port}")
        
        authorizer = DummyAuthorizer()

        current_dir = os.getcwd()
        ftp_root = os.path.join(current_dir, "ftp_share")
        if not os.path.exists(ftp_root):
            os.makedirs(ftp_root)
            logger.info(f"FTP_SERVER: Created FTP share directory: {ftp_root}")

        authorizer.add_user(self.username, self.password, ftp_root, perm="elradfmw") 
        authorizer.add_anonymous(ftp_root, perm="elr") 

        handler = ThrottledFTPHandler
        handler.authorizer = authorizer
        handler.banner = "Welcome to Probe Tool FTP Server" 
        handler.passive_ports = range(self.passive_ports_range[0], self.passive_ports_range[1] + 1)
        
        try:
            self.server = FTPServer((self.host, self.port), handler)
            logger.info(f"FTP_SERVER: Server ready to serve. Passive ports: {self.passive_ports_range}")
            
            self.server.serve_forever() 
        except Exception as e:
            logger.error(f"FTP_SERVER: Error starting or running FTP server: {e}")
        finally:
            self.stop() 
            logger.info("FTP_SERVER: Thread stopped.")

    def start(self):
        if self.running:
            logger.info("FTP_SERVER: Server is already running. No action taken by start().")
            return
        
        super().start() 
        logger.info("FTP_SERVER: Server starting...")

    def stop(self):
        if self.running:
            self.running = False 
            if self.server:
                ioloop = IOLoop.instance()
                if ioloop.running:
                    logger.info("FTP_SERVER: Scheduling IOLoop stop for FTP server.")
                    ioloop.add_callback(ioloop.stop)
                    self.server = None 
                else:
                    logger.info("FTP_SERVER: IOLoop not running, no explicit stop needed.")
                    self.server = None
            logger.info("FTP_SERVER: Server stopping.")
        else:
            logger.info("FTP_SERVER: Server is not running, no stop action needed.")
