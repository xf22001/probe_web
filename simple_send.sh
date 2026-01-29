#!/bin/bash

# 简单指令发送脚本
#
# 用法: ./simple_send.sh --ip <IP> --data "<fn> [params...]" [--stage <stage>]
#

# --- 配置 ---
PROBE_SERVER_URL="http://localhost:8000"

# --- 默认值 ---
DEVICE_IP=""
STAGE=0
TEXT_DATA=""

# --- 帮助信息 ---
show_help() {
    cat << EOF
简单指令发送脚本

通过数据字符串和可选的状态码发送指令, 模仿前端发送文本指令的逻辑.

用法: $0 [选项]

选项:
  -i, --ip <ip>        目标设备IP地址 (必需)
  -s, --stage <stage>    状态码 (可选, 数字, 默认为 0)
  -d, --data "<fn> [params...]"  指令和参数字符串 (必需).
                                     脚本会自动解析出第一个词作为功能码(fn),
                                     其余部分作为有效载荷.
  -h, --help             显示此帮助信息

示例:
  # fn=50, stage=0 (默认), payload="hello world"
  $0 -i 192.168.1.100 -d "50 hello world"

  # fn=50, stage=1, payload="other data"
  $0 -i 192.168.1.100 -s 1 -d "50 other data"
EOF
}

# --- 参数解析 ---
PARSED_ARGS=$(getopt -o i:s:d:h --long ip:,stage:,data:,help -n "$0" -- "$@")
if [[ $? -ne 0 ]]; then
    show_help >&2
    exit 1
fi
eval set -- "$PARSED_ARGS"

while true; do
    case "$1" in
        -i|--ip) DEVICE_IP="$2"; shift 2 ;;
        -s|--stage) STAGE="$2"; shift 2 ;;
        -d|--data) TEXT_DATA="$2"; shift 2 ;;
        -h|--help) show_help; exit 0 ;;
        --) 
            shift
            if [[ -n "$*" ]]; then
                if [[ -n "$TEXT_DATA" ]]; then
                    TEXT_DATA="$TEXT_DATA $*"
                else
                    TEXT_DATA="$*"
                fi
            fi
            break
            ;;
        *) echo "内部错误: 参数解析失败" >&2; exit 1 ;; 
    esac
done

# --- 参数校验 ---
if [[ -z "$DEVICE_IP" || -z "$TEXT_DATA" ]]; then
    echo "错误: --ip 和 --data 是必需参数." >&2
    show_help >&2
    exit 1
fi

if ! [[ "$STAGE" =~ ^-?[0-9]+$ ]]; then
    echo "错误: 状态码 (--stage) 必须是一个整数." >&2
    exit 1
fi

# --- 从 --data 字符串中解析出 fn (和可能的payload, 但payload在此次修改中不再剥离) ---
read -r FN_FROM_DATA _ <<< "$TEXT_DATA" # Read only the first word as FN_FROM_DATA

if [[ -z "$FN_FROM_DATA" ]]; then
    echo "错误: --data 字符串不能为空." >&2
    exit 1
fi

if ! [[ "$FN_FROM_DATA" =~ ^-?[0-9]+$ ]]; then
    echo "错误: --data 字符串的第一个词 ('$FN_FROM_DATA') 必须是功能码 (一个整数)." >&2
    exit 1
fi


# --- 执行 ---
echo "-------------------------------------"
echo "目标设备: $DEVICE_IP"
echo "解析的功能码 (fn): $FN_FROM_DATA"
echo "指定的状态码 (stage): $STAGE"
echo "完整指令字符串 (data): \"$TEXT_DATA\"" # 显示整个 TEXT_DATA
echo "-------------------------------------"
echo 

# 检查服务器连通性
if ! curl -s --connect-timeout 5 "$PROBE_SERVER_URL/api/scanner/status" >/dev/null 2>&1; then
    echo "错误: 无法连接到服务器 $PROBE_SERVER_URL" >&2
    echo "请确保Probe Web Tool服务正在运行。" >&2
    exit 1
fi

# 构建JSON负载
# 服务器将接收到明确的 fn, stage, 和 text (完整指令字符串)
JSON_DATA=$(printf '{"commands": [{"ip":"%s","text":"%s","fn":%d,"stage":%d}]}' "$DEVICE_IP" "$TEXT_DATA" "$FN_FROM_DATA" "$STAGE")

# 发送请求
echo "正在发送指令..."
RESPONSE=$(curl -s -w "\n%{http_code}" \
    -X POST "$PROBE_SERVER_URL/api/batch_send" \
    -H "Content-Type: application/json" \
    -d "$JSON_DATA")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
RESPONSE_BODY=$(echo "$RESPONSE" | sed '$d')

# 检查HTTP响应
if [[ $HTTP_CODE -eq 200 ]]; then
    echo "指令发送成功!"
    echo
    echo "服务器响应:"
    if command -v jq >/dev/null 2>&1; then
        echo "$RESPONSE_BODY" | jq '.'
    else
        echo "$RESPONSE_BODY"
    fi
else
    echo "错误: 请求失败, HTTP状态码: $HTTP_CODE" >&2
    echo "服务器响应:" >&2
    echo "$RESPONSE_BODY" >&2
    exit 1
fi

echo 

echo "操作完成."
