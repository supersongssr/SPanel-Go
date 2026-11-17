#!/bin/sh
#
# SPanel-Go git 自动部署脚本 (由 .git/hooks/post-commit & post-merge 调用)
#
# 流程: flock 串行化 → 记录旧二进制 SHA → run.sh build →
#       二进制有变化才 systemctl restart spanel-go → /ping 健康检查
#
# 设计决策:
#   * 编译失败: 保留旧二进制、不重启, 服务不受影响 (go build 仅成功时写产物)
#   * 二进制无变化 (仅文档/静态页改动): 跳过重启 —— public/ 磁盘直读本就即时生效
#   * 钩子始终 exit 0: 部署失败不影响 git 操作本身, 结果看日志与终端输出
#   * 日志: .bin/autodeploy.log (gitignored)
#
# 手动触发: ./scripts/git-autodeploy.sh manual

set -u

# 仓库根 = 本脚本所在目录的上一级 (脚本位于 scripts/)
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
cd "$REPO_ROOT"

TRIGGER="${1:-manual}"
LOG=".bin/autodeploy.log"
LOCK=".bin/autodeploy.lock"
BIN=".bin/spanel"
UNIT="spanel-go.service"
PORT="${APP_PORT:-8090}"

mkdir -p .bin


log() { printf '%s\n' "$*" | tee -a "$LOG"; }

# ── flock 串行化: 连续多次 commit 只排队不并发 ──────────────────────────
if ! exec 9>"$LOCK"; then
    echo "⚠️  无法创建锁文件 $LOCK, 跳过自动部署"
    exit 0
fi
if ! flock -w 120 9; then
    log "[$(date '+%F %T')] ⚠️  等待锁超时 (有其他部署在进行), 跳过本次 (trigger=$TRIGGER)"
    exit 0
fi

log "────────────────────────────────────────────"
log "[$(date '+%F %T')] 🚀 自动部署触发: $TRIGGER  HEAD=$(git rev-parse --short HEAD 2>/dev/null || echo '?')"

# ── 1. 编译 (失败则保留旧二进制并退出) ──────────────────────────────────
OLD_SHA=$(sha256sum "$BIN" 2>/dev/null | cut -d' ' -f1)

if ! ./run.sh build >>"$LOG" 2>&1; then
    log "[$(date '+%F %T')] ❌ 编译失败, 保留旧二进制, 服务未重启。编译日志见 $LOG"
    exit 0
fi

# ── 2. 二进制无变化则跳过重启 (仅文档/静态页改动) ────────────────────────
NEW_SHA=$(sha256sum "$BIN" 2>/dev/null | cut -d' ' -f1)
if [ -n "$OLD_SHA" ] && [ "$OLD_SHA" = "$NEW_SHA" ]; then
    log "[$(date '+%F %T')] ⏭️  二进制无变化 (非 Go 代码改动), 跳过重启"
    exit 0
fi

# ── 3. 重启服务 + 健康检查 ──────────────────────────────────────────────
if ! systemctl restart "$UNIT" 2>>"$LOG"; then
    log "[$(date '+%F %T')] ❌ systemctl restart $UNIT 失败, 请检查: journalctl -u $UNIT -n 20"
    exit 0
fi
sleep 1

if systemctl is-active --quiet "$UNIT" && curl -fsS --max-time 3 "http://127.0.0.1:$PORT/ping" >/dev/null 2>&1; then
    log "[$(date '+%F %T')] ✅ 部署完成: 新二进制已生效 (PID $(systemctl show -p MainPID --value "$UNIT")), /ping → pong"
else
    log "[$(date '+%F %T')] ⚠️  服务已重启但健康检查未通过, 请检查: journalctl -u $UNIT -n 20"
fi

exit 0
