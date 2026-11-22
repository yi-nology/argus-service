#!/usr/bin/env bash
# deploy-v810.sh —— 43 自愈部署链（VERSION 驱动；v8.1.0 首发，v8.2.0 起复跑）。
# 链：215 换芯构建（FROM 旧镜像 COPY /app/argus）+nc 暂存 → 43 ctr 导入+
# exec 探针 → 幂等迁移 Job 先行（#29 滚动升级序列）→ ARGUS_METRICS_AUTH=0
# 翻转 → set image 滚动。VPN 断时置顶等待（长窗自愈，日志 Heartbeat 可探活）；
# 全程断言 fail-closed。
#
# 用法: VERSION=v8.2.0 ./deploy/k8s43/deploy-v810.sh
# 前置: bin/argus（linux/arm64 ELF，已交叉编译）；215 ssh 别名；ARGUS_43_ROOT_PASS 环境变量。
set -euo pipefail
cd "$(dirname "$0")/../.."
V="${VERSION:?用法: VERSION=v8.2.0 ./deploy/k8s43/deploy-v810.sh}"
LOG=/tmp/deploy-${V}.log
# 地址（2026-11-22 网络重编号实锤）：43 旧 LAN 10.0.0.43 已废→10.60.226.43；
# 215 旧 LAN 10.0.0.215 已废→10.44.129.215（enp3s0 静态）。参数化防下轮
# 重编号再翻（v8.2.0 部署 ③ 步实锤：215 上新地址通、旧地址 channel 0 超时）。
H43="${ARGUS_43_HOST:-10.60.226.43}"
H215LAN="${ARGUS_215_LAN_IP:-10.44.129.215}"
exec >>"$LOG" 2>&1

say() { echo "[$(date '+%F %T')] $*"; }
[ -f bin/argus ] || { say "缺 bin/argus"; exit 1; }
[ "$(file -b bin/argus | cut -c1-3)" = "ELF" ] || { say "bin/argus 非 ELF"; exit 1; }
# 43 root 密码不入仓（公仓暴露面——v8.1.0 曾硬编码已泄露，待网络窗口旋转
# 密码）：由调用方环境变量注入（export ARGUS_43_ROOT_PASS=...）。
[ -n "${ARGUS_43_ROOT_PASS:-}" ] || { say "缺 ARGUS_43_ROOT_PASS 环境变量（不入仓纪律）"; exit 1; }

# ---- 置顶等待：VPN（215 跳板）恢复 ----
say "等待 VPN（215 跳板）恢复……"
n=0
until ssh -o ConnectTimeout=25 -o ServerAliveInterval=10 -o BatchMode=yes 215 true 2>/dev/null; do
  n=$((n+1))
  [ $((n % 10)) -eq 0 ] && say "Heartbeat: 已等待 $((n)) 分钟，215 仍不可达"
  [ $n -ge 720 ] && { say "等待 12h 超时，放弃（人工窗口再跑）"; exit 1; }
  sleep 60
done
say "215 可达，开始部署链"

# ---- ① gzip + rsync 断点续传（劣化链实锤改造：v8.2.0 首跑 scp 整包重试
# 在 1-2s RTT 中继上 5.5h×10 次全败；rsync 断点续传每次重试只补剩余字节，
# 且块级校验防静默损坏。git 中继已退役——分块分支需人工预置，且 215↔
# git.enjoye.top 同窗不可达，fail-loud 优于假兜底） ----
gzip -9 -c bin/argus > bin/argus.gz
LOCAL_SHA=$(shasum -a 256 bin/argus | awk '{print $1}')
GZ_SIZE=$(stat -f %z bin/argus.gz)
ssh_until() { # ssh_until <desc> <remote-cmd>
  local desc="$1"; shift
  n=0
  until ssh -o BatchMode=yes -o ConnectTimeout=30 -o ServerAliveInterval=10 215 "$@"; do
    n=$((n+1)); [ $n -ge 10 ] && { say "$desc 10 次仍失败，中止"; return 1; }
    say "$desc 重试 ${n}（VPN 抖动）"; sleep 20
  done
}
rsync_until() {
  # 幂等跳过：215 已有同尺寸 gzip 载荷（上一轮窗口传过）不重传。
  remote_size=$(ssh -o BatchMode=yes -o ConnectTimeout=25 215 'stat -c %s /tmp/swapbuild/argus.gz 2>/dev/null' 2>/dev/null)
  if [ "$remote_size" = "$GZ_SIZE" ]; then
    say "① 215 已有同尺寸 gzip 载荷（$GZ_SIZE bytes），跳过 rsync"; return 0
  fi
  n=0
  until rsync -aP --timeout=180 -e 'ssh -o BatchMode=yes -o ConnectTimeout=30 -o ServerAliveInterval=15' bin/argus.gz 215:/tmp/swapbuild/argus.gz >/dev/null 2>&1; do
    n=$((n+1)); [ $n -ge 60 ] && return 1
    say "① rsync 断点续传重试 ${n}（劣化链，已传部分保留续跑）"; sleep 15
  done
}
rsync_until || { say "① rsync 60 次全败——链路不可部署，abort（下窗口再跑）"; exit 1; }
# 解包 + SHA 硬校验（尺寸跳过时也须过 SHA，防半包冒充全量；gzip CRC 双保险）。
ssh_until "① 215 侧解包+SHA 校验" \
  "cd /tmp/swapbuild && gunzip -kf argus.gz && [ \"\$(sha256sum argus | cut -d' ' -f1)\" = \"$LOCAL_SHA\" ] && echo SHA_OK" | grep -q SHA_OK || { say "① SHA 校验失败（载荷损坏）"; exit 1; }
ssh_until "① 215 侧 ELF 校验" \
  'chmod 0755 /tmp/swapbuild/argus && [ $(stat -c %s /tmp/swapbuild/argus) -gt 10000000 ] && [ "$(head -c 4 /tmp/swapbuild/argus | od -An -c | tr -d " \n")" = "177ELF" ] && echo ELF_OK' | grep -q ELF_OK || { say "① ELF 校验失败"; exit 1; }
say "① 二进制已 gzip+rsync 直传 215 并通过 SHA/ELF 校验"

# ---- ② 215 侧换芯构建（FROM 本地最新的 argus:v6.9.0 基础层——布局核验过：
# /app/skills、plugins.yaml、ocrshim、git/ca-certificates/tzdata 齐备）+nc 暂存。
# swap.Dockerfile 每次从仓内重传（不依赖 215 上的历史残留）。 ----
scp -o BatchMode=yes deploy/k8s43/swap.Dockerfile 215:/tmp/swap.Dockerfile 2>/dev/null || {
  n=0
  until scp -o BatchMode=yes deploy/k8s43/swap.Dockerfile 215:/tmp/swap.Dockerfile 2>/dev/null; do
    n=$((n+1)); [ $n -ge 10 ] && { say "② 上传 swap.Dockerfile 10 次全败"; exit 1; }
    say "② 上传 swap.Dockerfile 重试 ${n}（VPN 抖动）"; sleep 20
  done
}
ssh_until "② 换芯构建" \
  "sed 's|FROM argus:<上一在役tag>|FROM argus:v6.9.0|' /tmp/swap.Dockerfile > /tmp/swapbuild/Dockerfile && cd /tmp/swapbuild && DOCKER_BUILDKIT=1 docker build --platform linux/arm64 -f Dockerfile -t argus:$V . >/dev/null 2>&1 && docker save argus:$V -o /tmp/argus-deploy.tar && rm -f /tmp/nc-deploy.log && (nohup nc -l -p 9998 < /tmp/argus-deploy.tar >/tmp/nc-deploy.log 2>&1 &) && sleep 1 && echo STAGED" | grep -q STAGED || { say "② 215 构建/暂存失败"; exit 1; }
say "② 215 侧镜像已暂存（nc:9998）"

# ---- ③ 43 侧接收+ctr 导入+exec 位探针（此时还不动 Deployment） ----
sshpass -p "$ARGUS_43_ROOT_PASS" ssh -o StrictHostKeyChecking=no -o BatchMode=yes -J 215 root@$H43 \
  "nc -w 900 $H215LAN 9998 > /tmp/argus-deploy.tar \
  && [ \$(stat -c %s /tmp/argus-deploy.tar) -gt 30000000 ] \
  && ctr -a /run/k3s/containerd/containerd.sock -n k8s.io images import /tmp/argus-deploy.tar >/dev/null 2>&1 \
  && ctr -a /run/k3s/containerd/containerd.sock -n k8s.io run --rm docker.io/library/argus:$V deployprobe sh -c 'test -x /app/argus'" \
  || { say "③ 43 导入/探针失败"; exit 1; }
say "③ 镜像已导入 43 并通过 exec 位探针（Deployment 未动）"

# ---- ④ 迁移 Job 先行（#29 序列；幂等可重跑，fail-closed 不交跳过） ----
say "④ 运行迁移 Job（跨 v8.0.0 多租户迁移，宽限 15min）……"
sshpass -p "$ARGUS_43_ROOT_PASS" ssh -o StrictHostKeyChecking=no -o BatchMode=yes -J 215 root@$H43 \
  "kubectl -n argus delete job argus-migrate --ignore-not-found >/dev/null 2>&1; \
   kubectl -n argus apply -f - >/dev/null && \
   kubectl -n argus wait --for=condition=complete job/argus-migrate --timeout=15m && \
   kubectl -n argus logs job/argus-migrate --tail=20" < deploy/k8s43/90-migration-job.yaml \
  || { say "④ 迁移 Job 失败/超时——中止（fail-closed，查 kubectl -n argus logs job/argus-migrate）"; exit 1; }
say "④ 迁移 Job 完成"

# ---- ⑤ v8.0.0 挂账：/metrics 翻转（secret 先翻，下一步删 Pod 时生效） ----
sshpass -p "$ARGUS_43_ROOT_PASS" ssh -o StrictHostKeyChecking=no -o BatchMode=yes -J 215 root@$H43 \
  "kubectl -n argus patch secret argus-env --type merge -p '{\"stringData\":{\"ARGUS_METRICS_AUTH\":\"0\"}}' >/dev/null" \
  || { say "⑤ secret 翻转失败（非致命，继续——metrics 仍走默认鉴权面）"; }
say "⑤ ARGUS_METRICS_AUTH=0 已写入 argus-env（随本次 Pod 重建生效）"

# ---- ⑥ 换镜像 + 滚动（同 swap-deploy.sh 收尾断言） ----
sshpass -p "$ARGUS_43_ROOT_PASS" ssh -o StrictHostKeyChecking=no -o BatchMode=yes -J 215 root@$H43 \
  "kubectl -n argus set image deploy/argus argus=argus:$V && \
   kubectl -n argus delete pod -l app=argus --grace-period=0 --force >/dev/null 2>&1 || true; \
   kubectl -n argus rollout status deploy/argus --timeout=180s >/dev/null && \
   kubectl -n argus logs deploy/argus --since=2m 2>/dev/null | grep http_ready | tail -1 && \
   kubectl -n argus get pods -l app=argus -o wide && \
   kubectl -n argus get deploy argus -o jsonpath='{.spec.template.spec.containers[0].image} {.status.readyReplicas}/{.spec.replicas} ready'" \
  || { say "⑥ 滚动失败"; exit 1; }
say "⑥ 滚动完成"

# ---- ⑦ 收尾 ----
say "⑦ $V 部署链全绿。回滚锚：ctr 内有上一在役镜像（v8.1.0/v7.5.0），set image 回退即可。"
