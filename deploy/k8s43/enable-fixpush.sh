#!/usr/bin/env bash
# enable-fixpush.sh —— 在 43 启用受控推送环（ARGUS_FIX_PUSH=confirm）。
# 语义：验证+审批通过的任务落 await_confirm，不自动推；由 maintainer 在
# /fix-push 页面 preview→confirm（绑定 expected_remote_head）后执行推送。
# 前置：ARGUS_FIX_VERIFY_CMD 已配置（没有验证环就不会产生 await_confirm 候选）。
# 用法：ssh root@10.60.226.43 'bash -s' < deploy/k8s43/enable-fixpush.sh
set -euo pipefail

NS="${NS:-argus}"
SECRET="${SECRET:-argus-env}"

echo "== 1/4 检查验证环配置（只报有无，不打印值）"
if kubectl -n "$NS" get secret "$SECRET" -o jsonpath='{.data.ARGUS_FIX_VERIFY_CMD}' | grep -q .; then
  echo "   ARGUS_FIX_VERIFY_CMD: 已配置"
else
  echo "   ARGUS_FIX_VERIFY_CMD: 未配置 —— confirm 环可开启但不会产生 await_confirm 候选"
fi

echo "== 2/4 写入 ARGUS_FIX_PUSH=confirm（保留 secret 其它键）"
kubectl -n "$NS" patch secret "$SECRET" --type=merge \
  -p '{"stringData":{"ARGUS_FIX_PUSH":"confirm"}}'

echo "== 3/4 滚动重启后端"
kubectl -n "$NS" rollout restart deploy/argus
kubectl -n "$NS" rollout status deploy/argus --timeout=300s

echo "== 4/4 状态"
kubectl -n "$NS" get pods -l app=argus -o wide --no-headers
echo "完成：GET /fix-push/preview 应返回 mode=confirm（需 maintainer/admin 档 token）。"