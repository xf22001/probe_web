# protocol.py
import struct
import base64
import logging # 导入 logging

# 获取当前模块的日志器
logger = logging.getLogger(__name__)

# Constants from C++ request.h
DEFAULT_REQUEST_MAGIC = 0xA5A55A5A

# Define struct format strings for packing/unpacking
# header_info_t: magic (I), total_size (I), data_size (I), data_offset (I), crc (B)
HEADER_FORMAT = "<IIIIB" # Little-endian, 4x unsigned int, 1x unsigned char
HEADER_SIZE = struct.calcsize(HEADER_FORMAT)

# payload_info_t: fn (I), stage (I)
PAYLOAD_INFO_FORMAT = "<II" # Little-endian, 2x unsigned int
PAYLOAD_INFO_SIZE = struct.calcsize(PAYLOAD_INFO_FORMAT)

REQUEST_T_SIZE = HEADER_SIZE + PAYLOAD_INFO_SIZE # Total size of request_t struct without data

def _calc_crc8(data: bytes) -> int:
    """
    Calculates a simple 8-bit checksum for a given byte sequence.
    NOTE: This is a simple byte sum, not a standard CRC8 algorithm.
    It matches the C++ `request_calc_crc8` function in `request.cpp`.
    """
    crc = 0
    for byte in data:
        crc = (crc + byte) & 0xFF # Ensure it stays 8-bit
    return crc

def encode_request(fn: int, stage: int, data: bytes) -> bytes:
    """
    Encodes a request according to the custom protocol.
    Mimics request_encode from request.cpp.
    """
    total_size = len(data)
    data_size = len(data) # Assuming the entire data is sent as one chunk
    data_offset = 0       # Assuming this is the first (and only) chunk

    # Pack payload_info_t
    payload_info_bytes = struct.pack(PAYLOAD_INFO_FORMAT, fn, stage)

    # Calculate CRC over payload_info_t and actual data
    crc_data = payload_info_bytes + data
    crc = _calc_crc8(crc_data)

    # Pack header_info_t
    header_bytes = struct.pack(HEADER_FORMAT, 
                               DEFAULT_REQUEST_MAGIC,
                               total_size,
                               data_size,
                               data_offset,
                               crc)

    logger.debug(f"Encoded request: fn={fn}, stage={stage}, data_size={len(data)}, crc={crc:02x}")
    return header_bytes + payload_info_bytes + data

def text_to_request_bytes(text: str) -> bytes:
    """Converts a text string into request data (e.g., for fn=0, stage=0)."""
    return encode_request(fn=0, stage=0, data=text.encode('utf-8'))

def base64_to_bytes(b64_string: str) -> bytes:
    """Decodes a base64 string to bytes."""
    try:
        return base64.b64decode(b64_string)
    except Exception as e:
        logger.error(f"Failed to decode base64 string: {b64_string[:50]}... Error: {e}")
        raise

def decode_request(buffer: bytes) -> tuple[dict, bytes] | None:
    """
    Decodes a request buffer according to the custom protocol.
    Mimics request_decode from request.cpp.
    Returns (header_payload_dict, actual_data_bytes) or None if invalid/incomplete.
    """
    if len(buffer) < REQUEST_T_SIZE:
        logger.debug(f"Decode Error: Incomplete buffer (len {len(buffer)} < {REQUEST_T_SIZE} bytes expected).")
        return None

    header_bytes = buffer[:HEADER_SIZE]
    payload_info_bytes = buffer[HEADER_SIZE:REQUEST_T_SIZE]
    
    header_info = struct.unpack(HEADER_FORMAT, header_bytes)
    magic, total_size, data_size, data_offset, crc_received = header_info

    if magic != DEFAULT_REQUEST_MAGIC:
        logger.debug(f"Decode Error: Magic invalid! Expected {DEFAULT_REQUEST_MAGIC:x}, got {magic:x}.")
        return None

    # Check if we have enough data for the declared data_size
    if len(buffer) < REQUEST_T_SIZE + data_size:
        logger.debug(f"Decode Error: Incomplete data payload. Buffer len {len(buffer)}, expected {REQUEST_T_SIZE + data_size} bytes.")
        return None
        
    actual_data_bytes = buffer[REQUEST_T_SIZE : REQUEST_T_SIZE + data_size]

    # Calculate CRC over payload_info_t and actual data
    crc_data = payload_info_bytes + actual_data_bytes
    crc_calculated = _calc_crc8(crc_data)

    if crc_received != crc_calculated:
        logger.debug(f"Decode Error: CRC invalid! Expected {crc_calculated:02x}, got {crc_received:02x}.")
        return None

    payload_info = struct.unpack(PAYLOAD_INFO_FORMAT, payload_info_bytes)
    fn, stage = payload_info
    
    request_data = {
        "magic": magic,
        "total_size": total_size,
        "data_size": data_size,
        "data_offset": data_offset,
        "crc": crc_received,
        "fn": fn,
        "stage": stage,
    }
    
    logger.debug(f"Decoded request: fn={fn}, stage={stage}, data_size={data_size}, total_size={total_size}.")
    return request_data, actual_data_bytes
