#!/usr/bin/env bash
set -euo pipefail

# xhs-mcp 生产部署脚本(identity-normalize + persistent profile)
# 默认 dry-run; 设 CONFIRM_DEPLOY=yes 才真正构建/备份/部署/重启.

APP_NAME="xhs-mcp"
REPO_DIR="/home/ubuntu/xiaohongshu-mcp"
DEPLOY_DIR="/opt/xhs-mcp"
BIN_NAME="xiaohongshu-mcp-linux-amd64"
PROFILE_PARENT="/opt/xhs-mcp/profile"
PROFILE_DIR="${PROFILE_PARENT}/chrome"

if [[ "${CONFIRM_DEPLOY:-}" != "yes" ]]; then
  echo "=== DRY RUN(未做任何改动)==="
  echo "设 CONFIRM_DEPLOY=yes 才会真正执行. 将会:"
  echo "  1. cd ${REPO_DIR} && go build ./... (先验证整体可编译)"
  echo "  2. go build -o /tmp/${BIN_NAME} . (构建主二进制)"
  echo "  3. 创建 ${PROFILE_DIR}(属主 ubuntu)"
  echo "  4. 备份 ${DEPLOY_DIR}/${BIN_NAME} -> *.bak-<时间戳>"
  echo "  5. 安装新二进制到 ${DEPLOY_DIR}/${BIN_NAME}"
  echo "  6. pm2 restart ${APP_NAME} --update-env (带 XHS_BROWSER_USER_DATA_DIR)"
  echo
  echo "当前分支: $(cd "$REPO_DIR" && git branch --show-current 2>/dev/null || echo '?')"
  echo "当前生产二进制: $(ls -la "${DEPLOY_DIR}/${BIN_NAME}" 2>/dev/null || echo '不存在')"
  exit 0
fi

echo "=== 真正部署中(CONFIRM_DEPLOY=yes)==="
cd "$REPO_DIR"

echo "[1/6] go build ./..."
go build ./...

echo "[2/6] 构建主二进制"
go build -o "/tmp/${BIN_NAME}" .

echo "[3/6] 准备 profile 目录(父层也要存在, 文件锁放父层)"
sudo mkdir -p "$PROFILE_DIR"
sudo chown -R ubuntu:ubuntu "$PROFILE_PARENT"

echo "[4/6] 备份旧二进制"
BACKUP="${DEPLOY_DIR}/${BIN_NAME}.bak-$(date +%F-%H%M%S)"
cp "${DEPLOY_DIR}/${BIN_NAME}" "$BACKUP"
echo "      备份 -> $BACKUP"

echo "[5/6] 安装新二进制"
cp "/tmp/${BIN_NAME}" "${DEPLOY_DIR}/${BIN_NAME}"

echo "[6/6] 重启 pm2(默认开启 normalize; 回滚见下)"
export XHS_BROWSER_USER_DATA_DIR="$PROFILE_DIR"
unset XHS_BROWSER_NORMALIZE
pm2 restart "$APP_NAME" --update-env

echo
echo "=== 部署完成 ==="
echo "回滚二进制(最彻底):"
echo "  cp '$BACKUP' '${DEPLOY_DIR}/${BIN_NAME}' && pm2 restart '$APP_NAME' --update-env"
echo "软回滚 A(关 normalize, 保留持久 profile):"
echo "  XHS_BROWSER_NORMALIZE=0 pm2 restart '$APP_NAME' --update-env"
echo "软回滚 B(关持久 profile, 回临时目录):"
echo "  pm2 restart '$APP_NAME' --update-env  # 先在 pm2 env 中移除 XHS_BROWSER_USER_DATA_DIR"
echo
echo "部署后建议: pm2 logs ${APP_NAME} --lines 50  观察是否有 profile lock / 登录态异常"
