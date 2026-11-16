#!/usr/bin/env bash
# sync-version.sh <vX.Y.Z> —— 版本单源派生（#65）：一条命令同步全部交付物口径，
# 下一次发布只改这一处入口。
#   1. Chart appVersion=vX.Y.Z、chart version=X.Y.Z（策略：跟随 appVersion 去 v）
#   2. README「当前版本」
#   3. CHANGELOG：回填缺失的 tag 段 + 目标版本段（prevTag..HEAD，conventional
#      commit 渲染，风格与 matrix-tools 生成器一致：### subject + body 转子弹）
#   4. docs/releases/vX.Y.Z.md 脚手架（已存在则不动，发布说明可另行充实）
# 幂等：重复执行结果一致。执行后跑 check-version-consistency.sh 应绿。
set -euo pipefail
cd "$(dirname "$0")/.."

if [ $# -ne 1 ] || ! [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "用法: $0 <vX.Y.Z>（如 v7.4.1）" >&2; exit 1
fi
target="$1"

python3 - "$target" << 'PY'
import re, subprocess, sys, datetime, os

target = sys.argv[1]

def run(*args):
    return subprocess.run(args, capture_output=True, text=True).stdout

# ---- 1：Chart appVersion + chart version（策略=跟随 appVersion 去 v 前缀） ----
p = 'deploy/helm/argus/Chart.yaml'
s = open(p).read()
s = re.sub(r'(?m)^appVersion: *"v[\d.]+"', f'appVersion: "{target}"', s, count=1)
s = re.sub(r'(?m)^version: *[\d.]+', f'version: {target[1:]}', s, count=1)
open(p, 'w').write(s)

# ---- 2：README 当前版本 ----
p = 'README.md'
s = open(p).read()
s = re.sub(r'当前版本 \*\*v\d+\.\d+\.\d+\*\*', f'当前版本 **{target}**', s, count=1)
open(p, 'w').write(s)

# ---- 3：CHANGELOG 回填缺失段（conventional commit 渲染，生成器同风格） ----
# 范围纪律：只补「比现有顶部段更新」的 tag（含目标版本）——历史老缺口
# （生成器未跑时期的段）属生成器遗留，不在本脚本越俎代庖。
tags = [t for t in run('git', 'tag', '--sort=v:refname').split()
        if re.fullmatch(r'v\d+\.\d+\.\d+', t)]
raw = open('CHANGELOG.md').read()
tops = re.findall(r'(?m)^## \[(v[\d.]+)\]', raw)
existing = set(tops)
top = max(tops, key=lambda v: [int(x) for x in v[1:].split('.')]) if tops else 'v0.0.0'

def newer(a, b):
    pa = [int(x) for x in a[1:].split('.')]; pb = [int(x) for x in b[1:].split('.')]
    return pa > pb

missing = [t for t in tags if t not in existing and newer(t, top)]
if target not in existing and newer(target, top):
    if target not in missing:
        missing.append(target)  # 发布前同步：目标段取 prevTag..HEAD
missing.sort(key=lambda v: [int(x) for x in v[1:].split('.')])  # 升序算 range

def section(tag, rng):
    date = run('git', 'log', '-1', '--format=%ad', '--date=short', tag).strip() \
        if tag in tags else datetime.date.today().isoformat()
    out = [f'## [{tag}] — {date}', '']
    # \x1e = 记录分隔（%b 多行体不能按换行切记录）；body 行已是清单则不再加缀
    log = run('git', 'log', '--format=%s\x1f%b\x1e', '--no-merges', rng)
    for rec in log.split('\x1e'):
        if not rec.strip():
            continue
        subj, _, body = rec.partition('\x1f')
        out += [f'### {subj.strip()}', '']
        for line in body.split('\n'):
            line = line.strip()
            if not line:
                continue
            out.append(line if line.startswith('- ') else f'- {line}')
        out.append('')
    return '\n'.join(out) + '\n'

if missing:
    m = re.search(r'(?m)^## \[', raw)
    header, body = raw[:m.start()], raw[m.start():]
    parts = [header]
    # 渲染按版本降序 → 顶部为最新（与既有 newest-first 排列一致）
    for t in sorted(missing, key=lambda v: [int(x) for x in v[1:].split('.')], reverse=True):
        if t in tags:
            i = tags.index(t)
            rng = f'{tags[i-1]}..{t}' if i > 0 else t
        else:
            prev = tags[-1] if tags else 'HEAD'
            rng = f'{prev}..HEAD'
        parts.append(section(t, rng))
    parts.append(body)
    open('CHANGELOG.md', 'w').write(''.join(parts))

# ---- 4：docs/releases 脚手架 ----
rel = f'docs/releases/{target}.md'
if not os.path.exists(rel):
    prev = tags[-1] if tags else 'HEAD'
    with open(rel, 'w') as f:
        f.write(f'# {target}\n\n> 发布说明脚手架（sync-version.sh 生成）；发布前请充实验收面。\n\n')
        f.write(run('git', 'log', '--oneline', f'{prev}..HEAD'))
print(f'已同步 {target}：Chart/README/CHANGELOG/releases')
PY

echo "下一步：充实 docs/releases/${target}.md，并运行 scripts/check-version-consistency.sh 验证。"
