#!/usr/bin/env bash
# upgrade-run.sh — 「桌面一键升级」的服务器端执行体。
#
# 为什么要有这一层：桌面那个 .bat 只能发一条 ssh 命令，塞不下成套的
# 校验与播报逻辑；把把关逻辑放服务器上，还顺带解决了引号/转义的地狱。
#
# 分工：
#   upgrade.sh   只管一件事：备份 → 原子替换 → 重启 → 冒烟自检（项目自带）。
#   本脚本       升级前把关 + 升级后播报：
#                  · 待装文件在不在
#                  · 指纹对不对（挡掉半截上传 / 被换过的文件）
#                  · 是不是已经装过了（重复双击直接返回，不做无谓重启）
#                  · 没有 TTY 时剥掉 ANSI 颜色，Windows 控制台才不显示乱码
#
# 用法：bash upgrade-run.sh <新二进制路径> [期望 md5] [--dry-run]
set -uo pipefail

NEWBIN="${1:-/tmp/wb2api-final}"
WANT="${2:-}"
DRY=0
[ "${3:-}" = "--dry-run" ] && DRY=1

APP_DIR=/opt/work2api
SVC=work2api

# 颜色只在真终端上开。ssh 非交互执行时 stdout 是管道，转义序列会以
# 原始字节进 Windows 控制台，变成 "←[1;36m" 这种乱码——所以按 TTY 降级。
if [ -t 1 ]; then
  C_RED=$'\033[1;31m'; C_GRN=$'\033[1;32m'; C_CYN=$'\033[1;36m'; C_OFF=$'\033[0m'
else
  C_RED=''; C_GRN=''; C_CYN=''; C_OFF=''
fi
say()   { printf '%s\n' "$*"; }
good()  { printf '%s\n' "${C_GRN}$*${C_OFF}"; }
bad()   { printf '%s\n' "${C_RED}$*${C_OFF}"; }
step()  { printf '%s\n' "${C_CYN}$*${C_OFF}"; }

step "【1/4】检查待装文件"
if [ ! -f "$NEWBIN" ]; then
  bad "  找不到 $NEWBIN"
  say "  请确认新二进制已上传到服务器 /tmp/。"
  exit 1
fi
GOT=$(md5sum "$NEWBIN" | awk '{print $1}')
say "  文件  $NEWBIN"
say "  指纹  $GOT"

step "【2/4】校验指纹"
if [ -n "$WANT" ] && [ "$GOT" != "$WANT" ]; then
  bad "  指纹不符，拒绝升级"
  say "    期望  $WANT"
  say "    实际  $GOT"
  say "  多半是上传没传完，或服务器上这份被换过。重新上传后再试。"
  exit 1
fi
if [ -n "$WANT" ]; then good "  指纹一致 ✓"; else say "  （未指定期望指纹，跳过比对）"; fi

step "【3/4】比对当前线上版本"
CUR=$(md5sum "$APP_DIR/wb2api" 2>/dev/null | awk '{print $1}')
say "  线上运行  $CUR"
if [ "$CUR" = "$GOT" ]; then
  good "  已经是这个版本了，无需升级（不做无谓重启）。"
  say ""
  say "  如果你是因为功能没生效才重跑：先按 Ctrl+F5 强制刷新面板。"
  exit 0
fi

# 服务账号必须读得了 auth 文件。这次的教训：新版本加载器会幂等回写 realm，
# 曾以 root 跑过一次就把 auths 全重写成 root 属主——服务用户读不了，
# 升级后 loaded 0 accounts，全 503。在这里挡住，而不是升完再 503。
SVC_USER=$(systemctl show -p User --value "$SVC" 2>/dev/null)
if [ -n "$SVC_USER" ] && [ "$SVC_USER" != "root" ]; then
  BAD_FILES=$(find "$APP_DIR/auths" -maxdepth 1 -name '*.json' ! -user "$SVC_USER" 2>/dev/null | wc -l)
  if [ "$BAD_FILES" -gt 0 ]; then
    bad "  auths 里有 $BAD_FILES 个文件不属于 $SVC_USER，服务读不到（升级后会 0 账号）"
    say "  先修属主再升级："
    say "    chown -R $SVC_USER:$SVC_USER $APP_DIR/auths"
    exit 1
  fi
  good "  auths 属主检查通过（$SVC_USER 可读）"
fi

if [ "$DRY" = "1" ]; then
  step "【4/4】--dry-run：检查全部通过，到此为止（未做任何改动）"
  exit 0
fi

step "【4/4】执行升级：备份 → 替换 → 重启 → 自检"
if [ -t 1 ]; then
  bash /tmp/upgrade.sh "$NEWBIN"
  RC=$?
else
  # 无 TTY：剥掉 upgrade.sh 的 ANSI 颜色再回传，Windows 控制台才干净
  bash /tmp/upgrade.sh "$NEWBIN" 2>&1 | sed -e 's/\x1b\[[0-9;]*m//g'
  RC=${PIPESTATUS[0]}
fi

echo
if [ "$RC" -eq 0 ]; then
  good "================ 升级完成 ================"
  say "  当前运行版本  $(md5sum "$APP_DIR/wb2api" | awk '{print $1}')"
  say "  服务状态      $(systemctl is-active "$SVC")"
  say ""
  say "  下一步：浏览器里按 Ctrl+F5 强制刷新面板，确认三件事——"
  say "    1. 侧边栏出现「积分构成」「任务中心」"
  say "    2. 积分构成页 = 一个号一张卡片 + 蓝色消耗条"
  say "    3. 账号页「添加账号」弹窗里有 国内版 / 国际版 单选"
  exit 0
fi

bad "================ 升级未通过（退出码 $RC）================"
say "  往上翻，输出里有具体原因。"
say ""
BAK=$(ls -1t "$APP_DIR"/wb2api.bak-* 2>/dev/null | head -1)
if [ -n "$BAK" ]; then
  say "  要回滚到升级前那一版，执行："
  say "    cp -p $BAK $APP_DIR/wb2api && systemctl restart $SVC"
else
  say "  没找到备份文件（$APP_DIR/wb2api.bak-*），请人工检查。"
fi
exit "$RC"
