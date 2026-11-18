#!/usr/bin/env bash
# check-version-consistency.sh —— 版本一致性门禁（#65）：
#   Chart appVersion == Chart version(去 v) == README 当前版本 == CHANGELOG 顶部
#   == docs/releases 最新；tag 构建时再 == git tag。Helm 模板镜像 tag 必须走
#   `.Values.image.tag | default .Chart.AppVersion` 同源（禁硬编码版本）。
# 任一不一致非零退出（CI 步骤红灯）。EXPECT_VERSION 可显式给定期望值。
set -euo pipefail
cd "$(dirname "$0")/.."

ver='v[0-9]+\.[0-9]+\.[0-9]+'
# 提取一律 grep -oE（BSD/GNU sed 对 BRE 的 "+" 支持不一致，不用 sed 正则）
app=$(grep -m1 -E '^appVersion:' deploy/helm/argus/Chart.yaml | grep -oE "\"${ver}\"" | tr -d '"')
chart=$(grep -m1 -E '^version:' deploy/helm/argus/Chart.yaml | grep -oE '[0-9][0-9.]*')
readme=$(grep -m1 -oE "当前版本 \*\*${ver}\*\*" README.md | grep -oE "$ver")
changelog=$(grep -m1 -oE "^## \[${ver}\]" CHANGELOG.md | grep -oE "$ver")
# releases 目录双形态：单入口批后归档至 docs/archive/releases/（兼容旧 docs/releases/）
release=$( { ls docs/releases 2>/dev/null; ls docs/archive/releases 2>/dev/null; } | grep -E "^${ver}\.md$" | sort -Vu | tail -1 | sed 's/\.md$//')

expect="${EXPECT_VERSION:-${GITHUB_REF_NAME:-}}"
fail=0
note() { printf '%s\n' "$*"; }
bad() { note "✗ $1 = ${2:-<空>}，期望 ${3:-<空>}"; fail=1; }

[ -n "$app" ]      || bad "Chart appVersion 未解析" "$app" "$expect"
[ -n "$readme" ]   || bad "README 当前版本未解析" "$readme" "$app"
[ -n "$changelog" ] || bad "CHANGELOG 顶部版本未解析" "$changelog" "$app"
[ -n "$release" ]  || bad "docs/releases 最新版本未解析" "$release" "$app"

[ "$readme" = "$app" ]    || bad "README 当前版本" "$readme" "$app"
[ "$changelog" = "$app" ] || bad "CHANGELOG 顶部版本" "$changelog" "$app"
[ "$release" = "$app" ]   || bad "docs/releases 最新" "$release" "$app"
[ "$chart" = "${app#v}" ] || bad "Chart version（策略=appVersion 去 v 前缀）" "$chart" "${app#v}"

# tag 构建再对账 git tag（main/PR 构建只查内部五处一致）
if [ -n "${GITHUB_REF:-}" ] && [ "${GITHUB_REF#refs/tags/}" != "$GITHUB_REF" ]; then
  [ "${GITHUB_REF#refs/tags/}" = "$app" ] || bad "git tag" "${GITHUB_REF#refs/tags/}" "$app"
fi

# 迁移 Job/主负载镜像 tag 同源纪律：模板里不得出现硬编码版本 tag
if grep -rnE 'tag: *"?v[0-9]+\.[0-9]+' deploy/helm/argus/templates/ >/dev/null 2>&1; then
  note "✗ Helm 模板存在硬编码镜像 tag（必须走 .Chart.AppVersion 同源）"
  grep -rnE 'tag: *"?v[0-9]+\.[0-9]+' deploy/helm/argus/templates/ || true
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  note "版本一致性检查失败（五处口径：Chart/README/CHANGELOG/releases${GITHUB_REF:+/tag}）"
  exit 1
fi
note "✓ 版本一致：${app}（chart ${chart}；readme/changelog/releases 同源）"
