#!/bin/bash

# SPanel-Go Service Management & Deployment Script
# Provides automated compilation, background execution management, and status monitoring.

APP_NAME="spanel-api"
PID_FILE="spanel.pid"
LOG_FILE="api.log"
GO_BIN="/usr/local/go/bin/go"

# Ensure /usr/local/go/bin is in the PATH
export PATH=$PATH:/usr/local/go/bin

# 1. Compilation function
build() {
    echo "========================================="
    echo "Compiling SPanel-Go API gateway..."
    echo "========================================="
    if [ -x "$GO_BIN" ]; then
        $GO_BIN build -o "$APP_NAME" ./cmd/api/
    else
        go build -o "$APP_NAME" ./cmd/api/
    fi
    
    if [ $? -eq 0 ]; then
        echo "✅ Compilation successful! Binary generated: ./$APP_NAME"
    else
        echo "❌ Compilation failed. Please check build errors above."
        exit 1
    fi
}

# 2. Start service function
start() {
    echo "========================================="
    echo "Starting SPanel-Go Service..."
    echo "========================================="
    
    # Check if already running
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            echo "⚠️  Service is already running (PID: $PID)."
            exit 0
        fi
    fi
    
    # Check if binary exists, compile if not
    if [ ! -f "$APP_NAME" ]; then
        echo "Binary not found. Triggering automated build first..."
        build
    fi
    
    # Launch in the background and store PID
    nohup "./$APP_NAME" > "$LOG_FILE" 2>&1 &
    PID=$!
    echo $PID > "$PID_FILE"
    disown $PID
    
    # Give it a second to start and check if it stayed alive
    sleep 1
    if ps -p "$PID" > /dev/null 2>&1; then
        echo "✅ Service successfully started in the background (PID: $PID)!"
        echo "Logs are being written to: ./$LOG_FILE"
    else
        echo "❌ Service failed to start. Printing recent logs below:"
        tail -n 20 "$LOG_FILE"
        rm -f "$PID_FILE"
    fi
}

# 3. Stop service function
stop() {
    echo "========================================="
    echo "Stopping SPanel-Go Service..."
    echo "========================================="
    
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            kill "$PID"
            echo "Sent SIGTERM to process $PID..."
            
            # Wait up to 10 seconds for graceful shutdown
            for i in {1..10}; do
                if ! ps -p "$PID" > /dev/null 2>&1; then
                    echo "✅ Service successfully stopped."
                    rm -f "$PID_FILE"
                    return 0
                fi
                sleep 1
            done
            
            # Force kill if graceful stop fails
            echo "⚠️  Graceful shutdown timed out. Forcing termination..."
            kill -9 "$PID"
            rm -f "$PID_FILE"
            echo "✅ Process forcefully terminated."
        else
            echo "⚠️  PID file exists but process is not running. Cleaning up stale PID file."
            rm -f "$PID_FILE"
        fi
    else
        # Fallback process search
        PID=$(pgrep -f "./$APP_NAME")
        if [ ! -z "$PID" ]; then
            echo "Found matching process(es): $PID. Stopping them..."
            kill $PID
            sleep 1
            kill -9 $PID >/dev/null 2>&1
            echo "✅ Process(es) stopped."
        else
            echo "ℹ️  Service is already stopped."
        fi
    fi
}

# 4. Status function
status() {
    echo "========================================="
    echo "SPanel-Go Service Status"
    echo "========================================="
    
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            UPTIME=$(ps -o etime= -p "$PID" | sed -e 's/^[ \t]*//')
            MEMORY=$(ps -o rss= -p "$PID" | awk '{print $1/1024 " MB"}')
            echo "🟢 Status: RUNNING"
            echo "Process ID (PID): $PID"
            echo "Uptime: $UPTIME"
            echo "Memory Usage: $MEMORY"
            echo "Active Listen Ports:"
            netstat -tulnp | grep "$PID" 2>/dev/null || ss -tulnp | grep "$PID"
            return 0
        else
            echo "🔴 Status: STOPPED (Stale PID file exists)"
            return 1
        fi
    else
        echo "🔴 Status: STOPPED"
        return 1
    fi
}

# 5. Show logs function
logs() {
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

# Help menu
usage() {
    echo "Usage: $0 {start|stop|restart|status|build|logs}"
    exit 1
}

# Parse commands
case "$1" in
    start)
        start
        ;;
    stop)
        stop
        ;;
    restart)
        stop
        sleep 1
        start
        ;;
    status)
        status
        ;;
    build)
        build
        ;;
    logs)
        logs
        ;;
    *)
        usage
        ;;
esac
