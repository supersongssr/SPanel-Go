#!/bin/bash

# SPanel-Go Service Management & Deployment Script (双实例架构)
#
#   user -> cmd/user  公网最简用户端  (auth/user/node/mu/link + 用户端静态页)
#   full -> cmd/full  全功能端        (用户端全部功能 + admin 路由与静态页, 建议内网/白名单部署)
#
# Usage: ./run.sh {build|start|stop|restart|status|logs} [user|full|all]

export PATH=$PATH:/usr/local/go/bin
GO_BIN="/usr/local/go/bin/go"

TARGET="${2:-all}"

# ---------- helpers ----------

svc_bin() { echo "spanel-$1"; }
svc_pid() { echo "spanel-$1.pid"; }
svc_log() { echo "$1.log"; }

# 在目标实例上执行函数: each <func>
each() {
    for t in user full; do
        [ "$TARGET" = "all" ] || [ "$TARGET" = "$t" ] || continue
        "$1" "$t"
    done
}

# ---------- build ----------

build_one() {
    local t=$1
    echo "========================================="
    echo "Compiling $t instance (./cmd/$t/) -> ./spanel-$t"
    echo "========================================="
    if [ -x "$GO_BIN" ]; then
        $GO_BIN build -o "spanel-$t" "./cmd/$t/"
    else
        go build -o "spanel-$t" "./cmd/$t/"
    fi
    if [ $? -eq 0 ]; then
        echo "✅ Compilation successful! Binary generated: ./spanel-$t"
    else
        echo "❌ Compilation failed for spanel-$t. Please check build errors above."
        exit 1
    fi
}

# ---------- start ----------

start_one() {
    local t=$1 bin pid
    bin="spanel-$t"
    echo "========================================="
    echo "Starting $t instance..."
    echo "========================================="

    if [ -f "$(svc_pid $t)" ]; then
        pid=$(cat "$(svc_pid $t)")
        if ps -p "$pid" > /dev/null 2>&1; then
            echo "⚠️  $t instance is already running (PID: $pid)."
            return
        fi
        rm -f "$(svc_pid $t)"
    fi

    if [ ! -f "$bin" ]; then
        echo "Binary ./$bin not found. Triggering automated build first..."
        build_one "$t"
    fi

    nohup "./$bin" > "$(svc_log $t)" 2>&1 &
    pid=$!
    echo "$pid" > "$(svc_pid $t)"
    disown "$pid"

    sleep 1
    if ps -p "$pid" > /dev/null 2>&1; then
        echo "✅ $t instance started (PID: $pid), logs: ./$(svc_log $t)"
    else
        echo "❌ $t instance failed to start. Printing recent logs below:"
        tail -n 20 "$(svc_log $t)"
        rm -f "$(svc_pid $t)"
    fi
}

# ---------- stop ----------

stop_one() {
    local t=$1 bin pid
    bin="spanel-$t"

    if [ -f "$(svc_pid $t)" ]; then
        pid=$(cat "$(svc_pid $t)")
        if ps -p "$pid" > /dev/null 2>&1; then
            echo "Stopping $t instance (PID: $pid)..."
            kill "$pid"
            for i in {1..10}; do
                ps -p "$pid" > /dev/null 2>&1 || break
                sleep 1
            done
            if ps -p "$pid" > /dev/null 2>&1; then
                echo "⚠️  Graceful shutdown timed out. Forcing termination..."
                kill -9 "$pid"
            fi
            echo "✅ $t instance stopped."
        else
            echo "⚠️  $t PID file exists but process is not running. Cleaning up stale PID file."
        fi
        rm -f "$(svc_pid $t)"
    else
        pid=$(pgrep -f "./$bin")
        if [ ! -z "$pid" ]; then
            echo "Found matching process(es) for $t: $pid. Stopping them..."
            kill $pid
            sleep 1
            kill -9 $pid > /dev/null 2>&1
            echo "✅ $t instance stopped."
        else
            echo "ℹ️  $t instance is already stopped."
        fi
    fi
}

# ---------- status ----------

status_one() {
    local t=$1 pid
    echo "========================================="
    echo "SPanel-Go [$t] Instance Status"
    echo "========================================="

    if [ -f "$(svc_pid $t)" ]; then
        pid=$(cat "$(svc_pid $t)")
        if ps -p "$pid" > /dev/null 2>&1; then
            echo "🟢 [$t] RUNNING (PID: $pid, Uptime: $(ps -o etime= -p "$pid" | tr -d ' '), Memory: $(ps -o rss= -p "$pid" | awk '{printf "%.1f MB", $1/1024}'))"
            ss -tulnp 2>/dev/null | grep "$pid" || netstat -tulnp 2>/dev/null | grep "$pid"
            return 0
        fi
        echo "🔴 [$t] STOPPED (Stale PID file exists)"
        return 1
    fi
    echo "🔴 [$t] STOPPED"
    return 1
}

# ---------- logs ----------

logs_one() {
    local t=$1 log
    log="$(svc_log $t)"
    if [ -f "$log" ]; then
        echo "========================================="
        echo "Recent Logs (tailing ./$log):"
        echo "========================================="
        tail -n 50 "$log"
        echo "========================================="
        echo "Tailing log file. Press Ctrl+C to exit."
        tail -f "$log"
    else
        echo "❌ Log file not found: ./$log"
    fi
}

# ---------- entry ----------

case "$1" in
    build)   each build_one ;;
    start)   each start_one ;;
    stop)    each stop_one ;;
    restart) each stop_one; sleep 1; each start_one ;;
    status)  each status_one ;;
    logs)
        case "$TARGET" in
            user|full) logs_one "$TARGET" ;;
            *) echo "Usage: $0 logs [user|full]"; exit 1 ;;
        esac
        ;;
    *)
        echo "Usage: $0 {build|start|stop|restart|status|logs} [user|full|all]"
        echo "  e.g. ./run.sh build          # 编译 user + full 两个实例"
        echo "       ./run.sh start user     # 仅启动用户端实例"
        echo "       ./run.sh restart full   # 仅重启全功能实例 (不影响 user)"
        exit 1
        ;;
esac
