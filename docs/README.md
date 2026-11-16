# Argus 文档索引

> 四层结构：`guide/` 用户与接入 · `ops/` 运维与交付 · `design/` 设计与契约 ·
> `archive/` 过程与历史。根入口：[README](../README.md) · [ARCHITECTURE](../ARCHITECTURE.md) ·
> [CHANGELOG](../CHANGELOG.md) · [CONTRIBUTING](../CONTRIBUTING.md) · [SECURITY](../SECURITY.md)。

## Guide（用户与接入）

| 文档 | 状态 | 说明 |
|---|---|---|
| [`guide/ide-mcp.md`](guide/ide-mcp.md) | 现行 | IDE / MCP 客户端接入 |
| [`guide/API-COMPAT.md`](guide/API-COMPAT.md) | 现行 | 对外契约稳定性分级与破坏性变更流程 |
| [`guide/plugin-authoring.md`](guide/plugin-authoring.md) | 现行 | 插件编写指南 |
| [`guide/github-app-setup.md`](guide/github-app-setup.md) | 现行 | GitHub App 配置 |
| [`guide/qconsole.md`](guide/qconsole.md) | 现行 | 质量控制台 CLI 用法 |

## Ops（运维与交付）

| 文档 | 状态 | 说明 |
|---|---|---|
| [`ops/deploy-production.md`](ops/deploy-production.md) | 现行 | 生产部署规范（Helm + 手工） |
| [`ops/runbook.md`](ops/runbook.md) | 现行 | 运维手册与故障排查 |
| [`ops/pilot.md`](ops/pilot.md) | 现行 | 试点上线与验收清单 |
| [`ops/chaos-matrix.md`](ops/chaos-matrix.md) | 现行 | 混沌测试矩阵 |
| [`ops/quality-ops.md`](ops/quality-ops.md) | 现行 | 质量运营口径 |
| [`ops/compaction-playbook.md`](ops/compaction-playbook.md) | 现行 | 上下文压缩处置手册 |
| [`ops/eval-baseline.md`](ops/eval-baseline.md) | 现行 | 评测基线口径 |
| [`ops/skills-release.md`](ops/skills-release.md) | 现行 | Skills 发布管理 |

## Design（设计与契约）

| 文档 | 状态 | 说明 |
|---|---|---|
| [`design/design.md`](design/design.md) | 设计契约 | 六轮评审定稿的原始设计 |
| [`design/architecture-deep-dive.md`](design/architecture-deep-dive.md) | 现行 | 架构演进深读 |
| [`design/review-closed-loop-design.md`](design/review-closed-loop-design.md) | 现行 | 审查闭环设计 |
| [`design/roadmap.md`](design/roadmap.md) | 现行 | v7.4–v8.0 演进路线 |
| [`design/tech-debt.md`](design/tech-debt.md) | 现行 | 工程债账本 |

## Archive（过程与历史）

- [`archive/deploy-215.md`](archive/deploy-215.md) — 215 单机部署历史（现役 43 k3s）
- [`archive/execution-13items.md`](archive/execution-13items.md) — 13 项执行清单
- [`archive/review-service-integration.md`](archive/review-service-integration.md) — review-service 集成（已停用）
- [`archive/reports/`](archive/reports/) — 验证/审计报告
- [`archive/releases/`](archive/releases/) — 历史发布说明
- [`archive/research/`](archive/research/) — 竞品调研
- `archive/superpowers/` — 历史 spec/plan（收尾归档）

## 旁路文档

- [`../deploy/README.md`](../deploy/README.md) — 部署资产目录说明
- [`../deploy/k8s/README-scaling.md`](../deploy/k8s/README-scaling.md) — 扩容说明
- `../knowledge/` — 运行时提示词资产（审查标准/安全检查单，不是用户文档）
- `../web/` — 宣传站（VuePress）
- `../eval/` — 评测集与实验记录

## 维护约定

- 新用户文档进 `guide/`，运维/交付进 `ops/`，设计与路线进 `design/`；完成的批次报告、
  一次性验证、被替代的部署文档进 `archive/`。
- `docs/superpowers/` 是 superpowers skill 的工作目录；批次收尾时整体归档到
  `docs/archive/superpowers/`。
- `make docs-check` 校验：生成物一致 + 本索引覆盖 guide/ops/design 全部 `.md` +
  非 archive 文档的相对 `.md` 链接可解析。