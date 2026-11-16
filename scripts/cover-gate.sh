#!/bin/sh
# 覆盖率门禁（#36 kernel 60/70）：全局下限 + 单包下限 + 棘轮防回退。
#
# 用法: cover-gate.sh <coverprofile> <global_min> <pkg_min> [floor_file]
#
# 口径（与 lint 排除 /biz/model/ 同源——hz/thrift 生成物不进门禁）：
#   - 全局：合并 profile 过滤 biz/ 后的语句覆盖 ≥ max(global_min, 棘轮值)。
#   - 单包：internal/… 有测试的包（组合根 internal/bootstrap 装配面豁免，
#     由 e2e 打桩与 arch 守卫兜底）语句覆盖 ≥ pkg_min。
#   - 棘轮：floor_file（默认 .cover-floor）记录已显式提交的全局下限，只升不降
#     ——覆盖率回退即挂；抬升 = 改 floor 文件并随全量验证一起提交。
set -eu

PROFILE=${1:?usage: cover-gate.sh <profile> <global_min> <pkg_min> [floor_file]}
GLOBAL_MIN=${2:?missing global_min}
PKG_MIN=${3:?missing pkg_min}
FLOOR_FILE=${4:-.cover-floor}

command -v go >/dev/null 2>&1 || { echo "需要 go 工具链" >&2; exit 1; }
[ -f "$PROFILE" ] || { echo "profile 不存在: $PROFILE" >&2; exit 1; }

floor=$(cat "$FLOOR_FILE" 2>/dev/null || echo 0)
case "$floor" in '' | *[!0-9]*) floor=0 ;; esac
if [ "$floor" -lt "$GLOBAL_MIN" ]; then
  floor=$GLOBAL_MIN
fi

go tool cover -func="$PROFILE" >/dev/null # profile 合法性预检（坏文件快速失败）

awk -v floor="$floor" -v pkgmin="$PKG_MIN" '
BEGIN { FS = " "; bad = 0; gstmts = 0; gcovered = 0 }
/^mode:/ { next }
/\/biz\// { next }        # 生成物（hz/thrift）不进门禁口径
{
  loc = $1
  sub(/:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/, "", loc)
  dir = loc
  sub(/\/[^\/]+$/, "", dir)
  split($2, m, " ")
  stmts = m[1] + 0
  cnt = $3 + 0
  gstmts += stmts
  if (cnt > 0) gcovered += stmts
  # 单包口径：internal/…（组合根 bootstrap 豁免）
  if (dir ~ /\/internal\// && dir !~ /\/internal\/bootstrap$/) {
    tstmts[dir] += stmts
    if (cnt > 0) tcovered[dir] += stmts
  }
}
END {
  if (gstmts == 0) { print "FAIL: profile 无有效语句（口径过滤后为空）" > "/dev/stderr"; exit 1 }
  gp = gcovered / gstmts * 100
  printf "global %.2f%% (floor %s%%)\n", gp, floor
  if (gp + 0 < floor + 0) {
    print "FAIL: 全局覆盖率低于棘轮下限（.cover-floor 只升不降；抬升请显式提交新下限）" > "/dev/stderr"
    bad = 1
  }
  for (d in tstmts) {
    p = tcovered[d] / tstmts[d] * 100
    printf "%-72s %6.1f%%\n", d, p
    if (p + 0 < pkgmin + 0 - 0.005) {
      printf "FAIL: 单包覆盖 %.1f%% < %s%%: %s\n", p, pkgmin, d > "/dev/stderr"
      bad = 1
    }
  }
  exit bad
}
' "$PROFILE"
