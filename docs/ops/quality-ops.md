# 质量运营口径（Phase 4）

> 状态：首批落地（2026-11-15）。spec：
> `docs/archive/superpowers/specs/2026-11-15-phase4-quality-ops-design.md`。
> 验收从「报告数量」转向可证伪指标——本文件是指标定义与纪律的单源。

## 指标定义

| 指标 | 定义 | 数据源 | 备注 |
|---|---|---|---|
| **有效发现率** | `verified + structurally_valid` 意见数 / 总意见数 | Phase 2 `Finding.EvidenceStatus` 四态 | 结构核验通过才算「有效」；unverified/invalid 不入分子 |
| **修复率** | `merged / (merged + open)` | Phase 3 `Task.FixStatus` + 平台 merge 查询 | `failed`/`not_tried` 不入分母（未达成不冒充）；merge 不可得记 open 保守计 |
| 批次环比 Δ | 相邻批次同口径差值（后−前） | `eval/results/*.json` 序列 | **跨口径切换线（Phase1②）禁直比**，报告自动标注 |
| 误报候选 | suppressed 指纹清单 | `finding_prints.status=suppressed` | 机器产候选，**人工合入金标准**（防污染红线） |
| **节流跳过回帖** | `reply_state=throttled` 任务数 | `tasks.reply_state`（v8.5.0） | 任务仍 done、报告已落库——只统计「回帖被频次窗挡住」的次数 |
| **级别静默** | 低于仓级 `severity_floor` 的意见条数 | `tasks.silenced_floor`（v8.5.0） | **静默≠没审**：审查/报告保留全量，报告尾注如实列出静默条数 |
| **上限静默** | 超出仓级 `findings_cap` 的意见条数 | `tasks.silenced_cap`（v8.5.0） | 按严重度降序取 top N，其余只留计数 |

回复控制三项（节流/级别静默/上限静默）同时经 `GET /stats/overview`
按仓返回（`reply_throttled` / `silenced_floor` / `silenced_cap`），前端
「仓库设置 → 近 30 天生效情况」直接消费——旋钮生效性可验证，不靠猜。

口径切换点纪律与批次归档见 `docs/ops/eval-baseline.md`（同源）。

## 入口

```bash
make quality-report   # 批次环比 → test/quality/results/quality-latest.md
make eval-candidates  # 误报候选清单 → test/quality/results/candidates-fp.md
```

- 两入口纯数据面不联网（quality-report 读 eval/results JSON；candidates
  缺省合成库测扫描逻辑，真库扫描走 `ARGUS_CANDIDATES_DSN` 环境接入——
  部署后 43 实弹项）；
- **红绿判定**：任一核心指标同口径环比恶化超阈值（ΔP < -0.05 量级）→
  该批次标黄，execution 清单须写归因——变差也要能解释。

## 边界（YAGNI）

- 维度趋势（repo/lang/model/plugin 分组）等有真实批次数据积累后接
  （spec §3.2，首期不造数）；
- 自动调参/实时告警不做（趋势先可见，策略人调）；
- 跨实例聚合=批 E（独立 spec）。
