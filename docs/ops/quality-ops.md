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
