#!/bin/bash

# SPanel-Go Service Management & Deployment Script (单实例全功能)
#
#   spanel -> cmd/spanel  全功能单体 (用户端 + 管理端 + 对外 API + 全部静态页)
#
# Usage: ./run.sh {build|start|stop|restart|status|logs}

export PATH=$PATH:/usr/local/go/bin
GO_BIN="/usr/local/go/bin/go"

BIN="spanel"
PID_FILE="spanel.pid"
LOG_FILE="spanel.log"

# ---------- build ----------
do_build() {
    echo "========================================="
    echo "Compiling SPanel-Go (./cmd/spanel/) -> ./$BIN"
    echo "========================================="
    if [ -x "$GO_BIN" ]; then
        $GO_BIN build -o "$BIN" "./cmd/spanel/"
    else
        go build -o "$BIN" "./cmd/spanel/"
    fi
    if [ $? -eq 0 ]; then
        echo "✅ Compilation successful! Binary generated: ./$BIN"
    else
        echo "❌ Compilation failed for $BIN. Please check build errors above."
        exit 1
    fi
}

# ---------- start ----------
do_start() {
    echo "========================================="
    echo "Starting SPanel-Go..."
    echo "========================================="

    if [ -f "$PID_FILE" ]; then
        pid=$(cat "$PID_FILE")
        if ps -p "$pid" > /dev/null 2>&1; then
            echo "⚠️  SPanel-Go is already running (PID: $pid)."
            return
        fi
        rm -f "$PID_FILE"
    fi

    if [ ! -f "$BIN" ]; then
        echo "Binary ./$BIN not found. Triggering automated build first..."
        do_build
    fi

    nohup "./$BIN" > "$LOG_FILE" 2>&1 &
    pid=$!
    echo "$pid" > "$PID_FILE"
    disown "$pid"

    sleep 1
    if ps -p "$pid" > /dev/null 2>&1; then
        echo "✅ SPanel-Go started (PID: $pid), logs: ./$LOG_FILE"
    else
        echo "❌ SPanel-Go failed to start. Printing recent logs below:"
        tail -n 20 "$LOG_FILE"
        rm -f "$PID_FILE"
    fi
}

# ---------- stop ----------
do_stop() {
    if [ -f "$PID_FILE" ]; then
        pid=$(cat "$PID_FILE")
        if ps -p "$pid" > /dev/null 2>&1; then
            echo "Stopping SPanel-Go (PID: $pid)..."
            kill "$pid"
            for i in {1..10}; do
                ps -p "$pid" > /dev/null 2>&1 || break
                sleep 1
            done
            if ps -p "$pid" > /dev/null 2>&1; then
                echo "⚠️  Graceful shutdown timed out. Forcing termination..."
                kill -9 "$pid"
            fi
            echo "✅ SPanel-Go stopped."
        else
            echo "⚠️  PID file exists but process is not running. Cleaning up stale PID file."
        fi
        rm -f "$PID_FILE"
    else
        pid=$(pgrep -x "$BIN")
        if [ ! -z "$pid" ]; then
            echo "Found matching process(es): $pid. Stopping them..."
            kill $pid
            sleep 1
            kill -9 $pid > /dev/null 2>&1
            echo "✅ SPanel-Go stopped."
        else
            echo "ℹ️  SPanel-Go is already stopped."
        fi
    fi
}

# ---------- status ----------
do_status() {
    echo "========================================="
    echo "SPanel-Go Status"
    echo "========================================="

    if [ -f "$PID_FILE" ]; then
        pid=$(cat "$PID_FILE")
        if ps -p "$pid" > /dev/null 2>&1; then
            echo "🟢 RUNNING (PID: $pid, Uptime: $(ps -o etime= -p "$pid" | tr -d ' '), Memory: $(ps -o rss= -p "$pid" | awk '{printf "%.1f MB", $1/1024}'))"
            ss -tulnp 2>/dev/null | grep "$pid" || netstat -tulnp 2>/dev/null | grep "$pid"
            return 0
        fi
        echo "🔴 STOPPED (Stale PID file exists)"
        return 1
    fi
    echo "🔴 STOPPED"
    return 1
}

# ---------- logs ----------
do_logs() {
    if [ -f "$LOG_FILE" ]; then
        echo "========================================="
        echo "Recent Logs (tailing ./$LOG_FILE):"
        echo "========================================="
        tail -n 50 "$LOG_FILE"
        echo "========================================="
        echo "Tailing log file. Press Ctrl+C to exit."
        tail -f "$LOG_FILE"
    else
        echo "❌ Log file not found: ./$LOG_FILE"
    fi
}

# ---------- entry ----------
case "$1" in
    build)   do_build ;;
    start)   do_start ;;
    stop)    do_stop ;;
    restart) do_stop; sleep 1; do_start ;;
    status)  do_status ;;
    logs)    do_logs ;;
    *)
        echo "Usage: $0 {build|start|stop|restart|status|logs}"
        echo "  e.g. ./run.sh build     # 编译 ./spanel"
        echo "       ./run.sh start     # 启动"
        echo "       ./run.sh logs      # 查看日志"
        exit 1
        ;;
esac
