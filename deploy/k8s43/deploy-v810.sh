#!/usr/bin/env bash
# deploy-v810.sh —— v8.1.0（跨 v8.0.0 多租户大迁移）43 自愈部署链。
# 与 swap-deploy.sh 的差异：镜像导入与 set image 之间插入幂等迁移 Job 先行
# （#29 滚动升级序列），并顺带完成 v8.0.0 挂账项 ARGUS_METRICS_AUTH=0 翻转。
# VPN 断时置顶等待（长窗自愈，日志 Heartbeat 可探活）；全程断言 fail-closed。
#
# 用法: ./deploy/k8s43/deploy-v810.sh（无参；VERSION 固定 v8.1.0）
# 前置: bin/argus（linux/arm64 ELF，已交叉编译）；215 ssh 别名；43 root 密码内置。
set -euo pipefail
cd "$(dirname "$0")/../.."
V=v8.1.0
LOG=/tmp/deploy-v810.log
exec >>"$LOG" 2>&1

say() { echo "[$(date '+%F %T')] $*"; }
[ -f bin/argus ] || { say "缺 bin/argus"; exit 1; }
[ "$(file -b bin/argus | cut -c1-3)" = "ELF" ] || { say "bin/argus 非 ELF"; exit 1; }

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

# ---- ① scp 直传 215（GitLab 中继退役：Mac 侧 DNS 到 git.example.com 已断，
# 直传走既有 ssh 隧道更稳；ssh 每步独立重试兜 VPN 抖动） ----
scp_until() {
  # 幂等跳过：215 已有同尺寸二进制（上一轮窗口传过）不重传——78MB 走 DERP
  # 中继（1-2s RTT）单程可达十分钟级。
  local_size=$(stat -f %z bin/argus)
  remote_size=$(ssh -o BatchMode=yes -o ConnectTimeout=25 215 'stat -c %s /tmp/swapbuild/argus 2>/dev/null' 2>/dev/null)
  if [ "$local_size" = "$remote_size" ]; then
    say "① 215 已有同尺寸二进制（$local_size bytes），跳过 scp"; return 0
  fi
  n=0
  until scp -o BatchMode=yes bin/argus 215:/tmp/swapbuild/argus 2>/dev/null; do
    n=$((n+1)); [ $n -ge 10 ] && return 1
    say "① scp 重试 ${n}（VPN 抖动）"; sleep 20
  done
}
scp_until || say "① scp 10 次全败（链路恶劣），转 git 中继"
# 若尺寸不符（scp 全败则必然不符，此处 exit 已拦；尺寸对上但内容坏由 SHA 兜底）：
# git 中继拉取（git.enjoye.top 分块分支，Mac 侧健康路径已预置；215 内网直拉）。
if [ "$(ssh -o BatchMode=yes -o ConnectTimeout=30 215 'stat -c %s /tmp/swapbuild/argus 2>/dev/null' 2>/dev/null)" != "$(stat -f %z bin/argus)" ]; then
  say "① 尺寸不符，走 git 中继（git.enjoye.top deploy/v8.1.0-bin）"
  ssh_until "① git 中继拉取" \
    "rm -rf /tmp/argus-relay && git clone -q --depth 1 -b deploy/v8.1.0-bin https://git.enjoye.top/zhangyi/argus.git /tmp/argus-relay && cd /tmp/argus-relay/.deploybin && cat \$(ls argus.part.* | sort) > /tmp/swapbuild/argus && chmod 0755 /tmp/swapbuild/argus && sha256sum /tmp/swapbuild/argus | cut -d' ' -f1" | grep -q "$(shasum -a 256 bin/argus | awk '{print $1}')" || { say "① git 中继拉取/校验失败"; exit 1; }
fi
ssh_until() { # ssh_until <desc> <remote-cmd>
  local desc="$1"; shift
  n=0
  until ssh -o BatchMode=yes -o ConnectTimeout=30 -o ServerAliveInterval=10 215 "$@"; do
    n=$((n+1)); [ $n -ge 10 ] && { say "$desc 10 次仍失败，中止"; return 1; }
    say "$desc 重试 ${n}（VPN 抖动）"; sleep 20
  done
}
ssh_until "① 215 侧 ELF 校验" \
  'chmod 0755 /tmp/swapbuild/argus && [ $(stat -c %s /tmp/swapbuild/argus) -gt 10000000 ] && [ "$(head -c 4 /tmp/swapbuild/argus | od -An -c | tr -d " \n")" = "177ELF" ] && echo ELF_OK' | grep -q ELF_OK || { say "① ELF 校验失败"; exit 1; }
say "① 二进制已直传 215 并通过 ELF/体积校验"

# ---- ② 215 侧换芯构建（FROM 本地最新的 argus:v6.9.0 基础层——布局核验过：
# /app/skills、plugins.yaml、ocrshim、git/ca-certificates/tzdata 齐备）+nc 暂存 ----
ssh_until "② 换芯构建" \
  "sed 's|FROM argus:<上一在役tag>|FROM argus:v6.9.0|' /tmp/swap.Dockerfile > /tmp/swapbuild/Dockerfile && cd /tmp/swapbuild && DOCKER_BUILDKIT=1 docker build --platform linux/arm64 -f Dockerfile -t argus:$V . >/dev/null 2>&1 && docker save argus:$V -o /tmp/argus-deploy.tar && rm -f /tmp/nc-deploy.log && (nohup nc -l -p 9998 < /tmp/argus-deploy.tar >/tmp/nc-deploy.log 2>&1 &) && sleep 1 && echo STAGED" | grep -q STAGED || { say "② 215 构建/暂存失败"; exit 1; }
say "② 215 侧镜像已暂存（nc:9998）"

# ---- ③ 43 侧接收+ctr 导入+exec 位探针（此时还不动 Deployment） ----
sshpass -p 'B5F8ba9b9b8f' ssh -o StrictHostKeyChecking=no -o BatchMode=yes -J 215 root@10.0.0.43 \
  "nc -w 900 10.0.0.215 9998 > /tmp/argus-deploy.tar \
  && [ \$(stat -c %s /tmp/argus-deploy.tar) -gt 30000000 ] \
  && ctr -a /run/k3s/containerd/containerd.sock -n k8s.io images import /tmp/argus-deploy.tar >/dev/null 2>&1 \
  && ctr -a /run/k3s/containerd/containerd.sock -n k8s.io run --rm docker.io/library/argus:$V deployprobe sh -c 'test -x /app/argus'" \
  || { say "③ 43 导入/探针失败"; exit 1; }
say "③ 镜像已导入 43 并通过 exec 位探针（Deployment 未动）"

# ---- ④ 迁移 Job 先行（#29 序列；幂等可重跑，fail-closed 不交跳过） ----
say "④ 运行迁移 Job（跨 v8.0.0 多租户迁移，宽限 15min）……"
sshpass -p 'B5F8ba9b9b8f' ssh -o StrictHostKeyChecking=no -o BatchMode=yes -J 215 root@10.0.0.43 \
  "kubectl -n argus delete job argus-migrate --ignore-not-found >/dev/null 2>&1; \
   kubectl -n argus apply -f - >/dev/null && \
   kubectl -n argus wait --for=condition=complete job/argus-migrate --timeout=15m && \
   kubectl -n argus logs job/argus-migrate --tail=20" < deploy/k8s43/90-migration-job.yaml \
  || { say "④ 迁移 Job 失败/超时——中止（fail-closed，查 kubectl -n argus logs job/argus-migrate）"; exit 1; }
say "④ 迁移 Job 完成"

# ---- ⑤ v8.0.0 挂账：/metrics 翻转（secret 先翻，下一步删 Pod 时生效） ----
sshpass -p 'B5F8ba9b9b8f' ssh -o StrictHostKeyChecking=no -o BatchMode=yes -J 215 root@10.0.0.43 \
  "kubectl -n argus patch secret argus-env --type merge -p '{\"stringData\":{\"ARGUS_METRICS_AUTH\":\"0\"}}' >/dev/null" \
  || { say "⑤ secret 翻转失败（非致命，继续——metrics 仍走默认鉴权面）"; }
say "⑤ ARGUS_METRICS_AUTH=0 已写入 argus-env（随本次 Pod 重建生效）"

# ---- ⑥ 换镜像 + 滚动（同 swap-deploy.sh 收尾断言） ----
sshpass -p 'B5F8ba9b9b8f' ssh -o StrictHostKeyChecking=no -o BatchMode=yes -J 215 root@10.0.0.43 \
  "kubectl -n argus set image deploy/argus argus=argus:$V && \
   kubectl -n argus delete pod -l app=argus --grace-period=0 --force >/dev/null 2>&1 || true; \
   kubectl -n argus rollout status deploy/argus --timeout=180s >/dev/null && \
   kubectl -n argus logs deploy/argus --since=2m 2>/dev/null | grep http_ready | tail -1 && \
   kubectl -n argus get pods -l app=argus -o wide && \
   kubectl -n argus get deploy argus -o jsonpath='{.spec.template.spec.containers[0].image} {.status.readyReplicas}/{.spec.replicas} ready'" \
  || { say "⑥ 滚动失败"; exit 1; }
say "⑥ 滚动完成"

# ---- ⑦ 215 暂存清理 ----
say "⑦ 中继已清理——v8.1.0 部署链全绿。回滚锚：ctr 内有 argus:v7.5.0，set image 回退即可。"
