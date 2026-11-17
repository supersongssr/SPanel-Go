#!/bin/sh
#
# SPanel-Go 开发 & 测试部署脚本 (POSIX /bin/sh, 菜单式)
#
#   * 编译 ./cmd/spanel → .bin/spanel   (二进制统一放 .bin/, 已加入 .gitignore)
#   * 本机运行 / 停止 / 重启 / 状态 / 日志 (默认端口 8090; config.yaml 的 8080 已被占用,
#     启动时以 -port 覆盖)
#   * 一键接管 test-spanel.freessr.bid 的 /api/ 路由:
#       nginx (8443) 注入 location ^~ /api/ → proxy_pass http://127.0.0.1:8090
#       其余路由仍由原 PHP 面板 (/var/www/SPanel/public) 提供, 可随时回滚
#   * 完整测试站 test-spanel-go.freessr.bid:8443 (独立 server 块, 全站 → 8090):
#       /etc/nginx/conf.d/test-spanel-go.freessr.bid.conf (静态页+api+mu+link 全由 Go 提供)
#
# 用法:
#   ./run.sh               # 无参数 → 交互菜单
#   ./run.sh build|start|stop|restart|status|logs|deploy|rollback
#
# 环境变量:
#   APP_PORT=8090          # SPanel-Go 监听端口 (deploy 时写入 nginx 代理片段)
#
set -u

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$SCRIPT_DIR"

# ---------- 基础配置 ----------
GO_BIN="/usr/local/go/bin/go"
command -v go >/dev/null 2>&1 && GO_BIN=$(command -v go)

BIN_DIR=".bin"
BIN="$BIN_DIR/spanel"
PID_FILE="$BIN_DIR/spanel.pid"
LOG_FILE="$BIN_DIR/spanel.log"

APP_PORT="${APP_PORT:-8090}"    # 监听端口 (8080 被本机其他进程占用, 默认 8090)

# systemd 守护 (推荐): /etc/systemd/system/spanel-go.service
#   开机自启 + 崩溃自动拉起 + journald 日志; run.sh 检测到 unit 存在时
#   自动改走 systemctl, 否则回退到 nohup + PID 文件模式。
SYSTEMD_UNIT="spanel-go.service"
SYSTEMD_OK=""

NGINX_CONF="/etc/nginx/conf.d/test-spanel.freessr.bid.conf"
NGINX_SITE="https://test-spanel.freessr.bid:8443"
NGINX_GO_CONF="/etc/nginx/conf.d/test-spanel-go.freessr.bid.conf"
GO_SITE="https://test-spanel-go.freessr.bid:8443"
MARK_BEGIN="# >>> spanel-go /api (managed by run.sh) >>>"
MARK_END="# <<< spanel-go /api <<<"

# ---------- 输出辅助 ----------
say()  { printf '%s\n' "$*"; }
ok()   { printf '✅ %s\n' "$*"; }
warn() { printf '⚠️  %s\n' "$*"; }
err()  { printf '❌ %s\n' "$*" >&2; }
hr()   { printf '%s\n' "========================================="; }

# 需要 root 时自动挂 sudo (修改 nginx 配置 / 重载)
require_sudo() {
    SUDO=""
    [ "$(id -u)" = "0" ] && return 0
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
        return 0
    fi
    err "此操作需要 root 权限 (修改/重载 nginx), 且未找到 sudo。"
    return 1
}

# 是否存在 systemd unit (结果缓存)
have_systemd() {
    [ -n "$SYSTEMD_OK" ] && { [ "$SYSTEMD_OK" = "1" ]; return; }
    if command -v systemctl >/dev/null 2>&1 && systemctl cat "$SYSTEMD_UNIT" >/dev/null 2>&1; then
        SYSTEMD_OK=1
        return 0
    fi
    SYSTEMD_OK=0
    return 1
}

# ---------- build ----------
do_build() {
    hr
    say "编译 SPanel-Go (./cmd/spanel) → ./$BIN"
    hr
    mkdir -p "$BIN_DIR"
    if ! "$GO_BIN" build -trimpath -ldflags "-s -w" -o "$BIN" ./cmd/spanel; then
        err "编译失败, 请检查上方错误输出。"
        return 1
    fi
    ok "编译成功: ./$BIN ($(du -h "$BIN" | cut -f1))"
    return 0
}

# ---------- 运行中 PID (无则返回 1) ----------
svc_pid() {
    if have_systemd; then
        _p=$(systemctl show -p MainPID --value "$SYSTEMD_UNIT" 2>/dev/null) || return 1
        [ -n "$_p" ] && [ "$_p" != "0" ] || return 1
        printf '%s' "$_p"
        return 0
    fi
    [ -f "$PID_FILE" ] || return 1
    _p=$(cat "$PID_FILE" 2>/dev/null) || return 1
    [ -n "$_p" ] || return 1
    kill -0 "$_p" 2>/dev/null || return 1
    printf '%s' "$_p"
}

# ---------- start ----------
do_start() {
    if have_systemd; then
        require_sudo || return 1
        if systemctl is-active --quiet "$SYSTEMD_UNIT"; then
            warn "$SYSTEMD_UNIT 已在运行 (PID: $(svc_pid))。"
            return 0
        fi
        $SUDO systemctl start "$SYSTEMD_UNIT"
        sleep 1
        if systemctl is-active --quiet "$SYSTEMD_UNIT"; then
            ok "已启动 (systemd: $SYSTEMD_UNIT, PID: $(svc_pid))"
            health_check_ping
            return 0
        fi
        err "启动失败, 最近日志:"
        journalctl -u "$SYSTEMD_UNIT" -n 20 --no-pager 2>/dev/null || \
            $SUDO systemctl --no-pager -l status "$SYSTEMD_UNIT" | tail -n 20
        return 1
    fi
    if _p=$(svc_pid); then
        warn "SPanel-Go 已在运行 (PID: $_p)。"
        return 0
    fi
    rm -f "$PID_FILE"
    if [ ! -f "$BIN" ]; then
        warn "未找到 ./$BIN, 先自动编译..."
        do_build || return 1
    fi
    hr
    say "启动 SPanel-Go (监听 :$APP_PORT)"
    hr
    nohup "$BIN" -port "$APP_PORT" > "$LOG_FILE" 2>&1 &
    _p=$!
    echo "$_p" > "$PID_FILE"
    sleep 1
    if ! kill -0 "$_p" 2>/dev/null; then
        err "启动失败, 最近日志:"
        tail -n 20 "$LOG_FILE" 2>/dev/null
        rm -f "$PID_FILE"
        return 1
    fi
    ok "已启动 (PID: $_p), 日志: ./$LOG_FILE"
    health_check_ping
    return 0
}

# 本地健康检查
health_check_ping() {
    command -v curl >/dev/null 2>&1 || return 0
    if curl -fsS --max-time 3 "http://127.0.0.1:$APP_PORT/ping" >/dev/null 2>&1; then
        ok "健康检查通过: GET /ping → pong"
    else
        warn "健康检查 /ping 未通过 (MySQL/Redis 可能仍在初始化)。"
    fi
    return 0
}

# ---------- stop ----------
do_stop() {
    if have_systemd; then
        require_sudo || return 1
        if systemctl is-active --quiet "$SYSTEMD_UNIT"; then
            $SUDO systemctl stop "$SYSTEMD_UNIT"
            ok "已停止 (systemd: $SYSTEMD_UNIT)。"
        else
            say "ℹ️  $SYSTEMD_UNIT 未在运行。"
        fi
        return 0
    fi
    if _p=$(svc_pid); then
        say "停止 SPanel-Go (PID: $_p)..."
        kill "$_p" 2>/dev/null
        _i=0
        while [ "$_i" -lt 10 ]; do
            kill -0 "$_p" 2>/dev/null || break
            sleep 1
            _i=$((_i + 1))
        done
        if kill -0 "$_p" 2>/dev/null; then
            warn "优雅关闭超时, 强制结束..."
            kill -9 "$_p" 2>/dev/null
        fi
        rm -f "$PID_FILE"
        ok "已停止。"
        return 0
    fi
    rm -f "$PID_FILE"
    # PID 文件丢失时兜底
    _pids=$(pgrep -x spanel 2>/dev/null || true)
    if [ -n "$_pids" ]; then
        warn "PID 文件缺失但发现残留进程: $_pids, 一并停止。"
        kill $_pids 2>/dev/null || true
        sleep 1
        kill -9 $_pids 2>/dev/null || true
        ok "已停止。"
    else
        say "ℹ️  SPanel-Go 未在运行。"
    fi
    return 0
}

# ---------- status ----------
api_status() {
    if [ -f "$NGINX_CONF" ] && grep -qF "$MARK_BEGIN" "$NGINX_CONF" 2>/dev/null; then
        say "🟢 /api/ 已由 SPanel-Go 接管 (location ^~ /api/ → 127.0.0.1:$APP_PORT)"
    else
        say "⚪ /api/ 由原 PHP 面板处理 (未接管)"
    fi
}

do_status() {
    hr
    say "SPanel-Go 服务状态"
    hr
    if _p=$(svc_pid); then
        say "🟢 运行中 (PID: $_p, 启动时长: $(ps -o etime= -p "$_p" | tr -d ' '), 内存: $(ps -o rss= -p "$_p" | awk '{printf "%.1f MB", $1/1024}'))"
        command -v ss >/dev/null 2>&1 && ss -tlnp 2>/dev/null | grep ":$APP_PORT " || true
    else
        say "🔴 已停止"
    fi
    hr
    say "nginx /api/ 接管状态 ($NGINX_CONF)"
    hr
    api_status
    if [ -f "$NGINX_GO_CONF" ]; then
        say "🟢 完整测试站: $GO_SITE (全站 → 127.0.0.1:$APP_PORT, 配置 $NGINX_GO_CONF)"
    else
        say "⚪ 完整测试站未部署 (缺 $NGINX_GO_CONF)"
    fi
    if have_systemd; then
        say "守护: systemd $SYSTEMD_UNIT ($(systemctl is-enabled "$SYSTEMD_UNIT" 2>/dev/null || echo unknown), "\
"累计自动重启: $(systemctl show -p NRestarts --value "$SYSTEMD_UNIT" 2>/dev/null))"
    else
        say "守护: nohup + $PID_FILE (未安装 systemd unit)"
    fi
}

# ---------- logs ----------
do_logs() {
    if have_systemd; then
        hr
        say "最近日志 (journalctl -u $SYSTEMD_UNIT):"
        hr
        journalctl -u "$SYSTEMD_UNIT" -n 50 --no-pager 2>/dev/null || \
            $SUDO journalctl -u "$SYSTEMD_UNIT" -n 50 --no-pager
        say "—— 以下实时跟踪, 按 Ctrl+C 返回 ——"
        journalctl -u "$SYSTEMD_UNIT" -f 2>/dev/null || \
            $SUDO journalctl -u "$SYSTEMD_UNIT" -f
        return 0
    fi
    if [ -f "$LOG_FILE" ]; then
        hr
        say "最近日志 (./$LOG_FILE):"
        hr
        tail -n 50 "$LOG_FILE"
        say "—— 以下实时跟踪, 按 Ctrl+C 返回 ——"
        tail -f "$LOG_FILE"
    else
        err "日志文件不存在: ./$LOG_FILE"
    fi
}

# ---------- nginx /api/ 接管 ----------
# 从 $1 中剥离旧的接管片段 (按标记, 幂等)
strip_api_block() {
    awk -v b="$MARK_BEGIN" -v e="$MARK_END" '
        index($0, b) > 0 { skip = 1 }
        skip { if (index($0, e) > 0) skip = 0; next }
        { print }
    ' "$1"
}

# 生成 /api/ 代理片段到 $1
make_snippet() {
    cat > "$1" <<EOF
    $MARK_BEGIN
    location ^~ /api/ {
        proxy_pass http://127.0.0.1:$APP_PORT;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_connect_timeout 5s;
        proxy_read_timeout 60s;
        proxy_send_timeout 60s;
    }
    $MARK_END
EOF
}

# 部署: 注入 /api/ 代理片段到 ssl server 块 (最后一个 ^} 之前)
do_deploy() {
    require_sudo || return 1
    if [ ! -f "$NGINX_CONF" ]; then
        err "未找到 nginx 配置: $NGINX_CONF"
        return 1
    fi
    command -v nginx >/dev/null 2>&1 || { err "未找到 nginx 命令。"; return 1; }
    command -v mktemp >/dev/null 2>&1 || { err "未找到 mktemp 命令。"; return 1; }

    if ! _p=$(svc_pid); then
        warn "SPanel-Go 未在运行, 接管后 /api/ 将返回 502。建议先启动 (菜单 3)。"
    fi

    _bak="$NGINX_CONF.bak.$(date +%Y%m%d%H%M%S)"
    $SUDO cp "$NGINX_CONF" "$_bak"
    say "已备份: $_bak"

    _tmp_clean=$(mktemp); _tmp_out=$(mktemp); _tmp_snip=$(mktemp)

    # 1) 剥离旧片段 (幂等, 重复执行安全)
    strip_api_block "$NGINX_CONF" > "$_tmp_clean"

    # 2) 生成新片段并插入到 ssl server 块末尾 (文件最后一个顶格 })
    make_snippet "$_tmp_snip"
    if ! awk -v snip="$_tmp_snip" '
        { L[NR] = $0 }
        END {
            last = 0
            for (i = NR; i >= 1; i--) if (L[i] ~ /^}[[:space:]]*$/) { last = i; break }
            if (last == 0) { print "ERROR: 未找到 server 块结束符 }" > "/dev/stderr"; exit 1 }
            for (i = 1; i <= NR; i++) {
                if (i == last) while ((getline line < snip) > 0) print line
                print L[i]
            }
        }' "$_tmp_clean" > "$_tmp_out"; then
        err "插入代理片段失败。"
        rm -f "$_tmp_clean" "$_tmp_out" "$_tmp_snip"
        return 1
    fi

    # 3) 覆盖 → 校验 → 重载 (校验失败自动还原)
    $SUDO cp "$_tmp_out" "$NGINX_CONF"
    rm -f "$_tmp_clean" "$_tmp_out" "$_tmp_snip"
    if ! _out=$($SUDO nginx -t 2>&1); then
        err "nginx -t 校验失败, 还原备份配置..."
        err "$_out"
        $SUDO cp "$_bak" "$NGINX_CONF"
        return 1
    fi
    $SUDO nginx -s reload
    ok "/api/ 已接管 → http://127.0.0.1:$APP_PORT (其余路由仍走 PHP)"

    verify_public
    return 0
}

# 回滚: /api/ 交还给 PHP 面板
do_rollback() {
    require_sudo || return 1
    if [ ! -f "$NGINX_CONF" ]; then
        err "未找到 nginx 配置: $NGINX_CONF"
        return 1
    fi
    if ! grep -qF "$MARK_BEGIN" "$NGINX_CONF" 2>/dev/null; then
        say "ℹ️  /api/ 本来就未被接管, 无需回滚。"
        return 0
    fi
    _bak="$NGINX_CONF.bak.$(date +%Y%m%d%H%M%S)"
    $SUDO cp "$NGINX_CONF" "$_bak"
    say "已备份: $_bak"

    _tmp=$(mktemp)
    strip_api_block "$NGINX_CONF" > "$_tmp"
    $SUDO cp "$_tmp" "$NGINX_CONF"
    rm -f "$_tmp"

    if ! _out=$($SUDO nginx -t 2>&1); then
        err "nginx -t 校验失败, 还原备份配置..."
        err "$_out"
        $SUDO cp "$_bak" "$NGINX_CONF"
        return 1
    fi
    $SUDO nginx -s reload
    ok "/api/ 已交还给 PHP 面板处理。"
    return 0
}

# 公网验证 (DNS 不指向本机时可忽略)
verify_public() {
    command -v curl >/dev/null 2>&1 || return 0
    say "验证: GET $NGINX_SITE/api/v2/backend/healthz"
    if _body=$(curl -ksS --max-time 5 "$NGINX_SITE/api/v2/backend/healthz" 2>&1); then
        say "响应: $_body"
        ok "公网 /api/ 已由 SPanel-Go 提供。"
    else
        warn "请求失败: $_body (若域名 DNS 未指向本机可忽略)"
    fi
}

# ---------- restart ----------
do_restart() {
    if have_systemd; then
        require_sudo || return 1
        $SUDO systemctl restart "$SYSTEMD_UNIT"
        sleep 1
        if systemctl is-active --quiet "$SYSTEMD_UNIT"; then
            ok "已重启 (systemd: $SYSTEMD_UNIT, PID: $(svc_pid))"
            health_check_ping
            return 0
        fi
        err "重启失败, 最近日志:"
        journalctl -u "$SYSTEMD_UNIT" -n 20 --no-pager 2>/dev/null
        return 1
    fi
    do_stop
    sleep 1
    do_start
}

# ---------- 一键: 编译 → 重启 → 接管 ----------
do_one_click() {
    do_build || return 1
    do_restart || return 1
    do_deploy
}

# ---------- 菜单 ----------
show_menu() {
    hr
    say " SPanel-Go 管理菜单   服务: 127.0.0.1:$APP_PORT   二进制: ./$BIN"
    if [ -f "$NGINX_CONF" ] && grep -qF "$MARK_BEGIN" "$NGINX_CONF" 2>/dev/null; then
        say " /api/ 接管状态: 🟢 SPanel-Go"
    else
        say " /api/ 接管状态: ⚪ PHP"
    fi
    hr
    say "  1) 一键部署   (编译 → 重启服务 → nginx 接管 /api/)"
    say "  2) 仅编译     (→ ./$BIN)"
    say "  3) 启动服务"
    say "  4) 停止服务"
    say "  5) 重启服务"
    say "  6) 状态       (服务 + /api/ 接管状态)"
    say "  7) 日志       (tail -f, Ctrl+C 返回)"
    say "  8) nginx 接管 /api/  ($NGINX_SITE → SPanel-Go)"
    say "  9) 回滚 /api/        (交还给 PHP 面板)"
    say "  0) 退出"
    hr
    printf "请选择: "
}

menu() {
    while :; do
        show_menu
        read -r _choice || break
        case "$_choice" in
            1) do_one_click ;;
            2) do_build ;;
            3) do_start ;;
            4) do_stop ;;
            5) do_restart ;;
            6) do_status ;;
            7) do_logs ;;
            8) do_deploy ;;
            9) do_rollback ;;
            0|q|Q) break ;;
            *) warn "无效选项: $_choice" ;;
        esac
        printf '\n按回车返回菜单... '
        read -r _dummy || break
        printf '\n\n'
    done
}

usage() {
    cat <<EOF
用法: $0 [命令]        (无参数 = 交互菜单)

  build     编译 → .bin/spanel
  start     启动服务 (端口 $APP_PORT, 可用 APP_PORT 环境变量覆盖)
  stop      停止服务
  restart   重启服务
  status    状态 (服务 + /api/ 接管状态)
  logs      查看日志
  deploy    nginx 接管 test-spanel.freessr.bid 的 /api/ → SPanel-Go
  rollback  /api/ 交还给 PHP 面板

完整测试站: https://test-spanel-go.freessr.bid:8443/ (全站由 SPanel-Go 提供,
           配置: $NGINX_GO_CONF)
EOF
}

# ---------- 入口 ----------
case "${1:-menu}" in
    build)    do_build ;;
    start)    do_start ;;
    stop)     do_stop ;;
    restart)  do_restart ;;
    status)   do_status ;;
    logs)     do_logs ;;
    deploy)   do_deploy ;;
    rollback) do_rollback ;;
    menu|"")  menu ;;
    -h|--help|help) usage ;;
    *) usage; exit 1 ;;
esac
