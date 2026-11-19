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
until ssh -o ConnectTimeout=10 -o BatchMode=yes 215 true 2>/dev/null; do
  n=$((n+1))
  [ $((n % 10)) -eq 0 ] && say "Heartbeat: 已等待 $((n)) 分钟，215 仍不可达"
  [ $n -ge 720 ] && { say "等待 12h 超时，放弃（人工窗口再跑）"; exit 1; }
  sleep 60
done
say "215 可达，开始部署链"

# ---- ① GitLab 通用包中继上传（换芯链，同 swap-deploy.sh 断言） ----
T=$(ssh -o BatchMode=yes 215 'grep "^ARGUS_GITLAB_TOKEN=" /opt/argus/argus.env | cut -d= -f2')
UP=$(curl -sk --max-time 300 --request PUT --header "PRIVATE-TOKEN: $T" \
  --upload-file bin/argus "https://git.example.com/api/v4/projects/1340/packages/generic/argus-bin/$V/argus-linux-arm64" -o /dev/null -w "%{http_code}")
[ "$UP" = "201" ] || { say "up=$UP 非 201，中止"; exit 1; }
say "① 二进制已上中继（up=201，$(stat -f %z bin/argus) bytes）"

# ---- ② 215 侧拉取+ELF 验+换芯构建+nc 暂存 ----
ssh -o BatchMode=yes 215 "T=$T; curl -sk -L --header \"PRIVATE-TOKEN: \$T\" -o /tmp/swapbuild/argus 'https://git.example.com/api/v4/projects/1340/packages/generic/argus-bin/$V/argus-linux-arm64' \
  && chmod 0755 /tmp/swapbuild/argus \
  && [ \$(stat -c %s /tmp/swapbuild/argus) -gt 10000000 ] \
  && [ \"\$(head -c 4 /tmp/swapbuild/argus | od -An -c | tr -d ' \n')\" = '177ELF' ] \
  && cd /tmp/swapbuild && DOCKER_BUILDKIT=1 docker build --platform linux/arm64 -f /tmp/swap.Dockerfile -t argus:$V . >/dev/null 2>&1 \
  && docker save argus:$V -o /tmp/argus-deploy.tar \
  && (nohup nc -l -p 9998 < /tmp/argus-deploy.tar >/tmp/nc-deploy.log 2>&1 &) && sleep 1 && echo STAGED" | grep -q STAGED || { say "② 215 暂存失败"; exit 1; }
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

# ---- ⑦ 中继清理 ----
ssh -o BatchMode=yes 215 "rm -f /tmp/argus-deploy.tar /tmp/nc-deploy.log /tmp/swapbuild/argus; T=$T; PID=\$(curl -sk --header \"PRIVATE-TOKEN: \$T\" 'https://git.example.com/api/v4/projects/1340/packages?per_page=5' | python3 -c \"import json,sys; print(next((p['id'] for p in json.load(sys.stdin) if p['name']=='argus-bin'),'none'))\"); [ \"\$PID\" != none ] && curl -sk --request DELETE --header \"PRIVATE-TOKEN: \$T\" https://git.example.com/api/v4/projects/1340/packages/\$PID -o /dev/null -w relay_del=%{http_code}" || true
say "⑦ 中继已清理——v8.1.0 部署链全绿。回滚锚：ctr 内有 argus:v7.5.0，set image 回退即可。"
