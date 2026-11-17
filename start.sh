#!/bin/sh
set -e

# 未指定外部 guacd 时，尝试在本容器内启动（镜像内未装 guacd 则跳过）
if [ -z "$GUACD_ADDR" ]; then
    GUACD=$(command -v guacd 2>/dev/null || true)
    if [ -n "$GUACD" ]; then
        echo "Starting local guacd: $GUACD"
        "$GUACD" -b 127.0.0.1 -l 4822 -f &
        export GUACD_ADDR=127.0.0.1:4822
    else
        echo "WARNING: 未找到 guacd，且未设置 GUACD_ADDR，RDP 不可用"
    fi
else
    echo "Using external guacd: $GUACD_ADDR"
fi

# Start the app
exec /app/remote-connect-manager
