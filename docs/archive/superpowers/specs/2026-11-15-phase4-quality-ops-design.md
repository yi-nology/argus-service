# Argus Phase 4 质量运营设计 spec

- 日期：2026-11-15
- 状态：设计稿（待用户批准后出实施 plan）
- 上游：v7.1 spec §5 骨架的细化；依赖 Phase 1（口径三分）/ Phase 2（Evidence
  四态）/ Phase 3（修复环状态机）全部已落地
- 关联：`docs/superpowers/specs/2026-11-14-v71-quality-calibration-design.md`
  （总纲）、`docs/execution-13items.md`（批 E 折入本阶段）

---

## 0. 背景：为什么现在做

前三阶段把「度量的地基」修完了：

| 阶段 | 交付 | 给 Phase 4 的地基 |
|---|---|---|
| Phase 1 | 评测口径三分（Unlabeled/CleanCaseFPRate/受限 Precision） | 指标不再系统性高估——趋势曲线可比 |
| Phase 2 | Evidence 四态回写（invalid/unverified/structural/verified） | 每条意见带服务端核验状态——「有效发现」可定义 |
| Phase 3 | 修复环按 PR 收敛+状态机（pushed/pr_created/cr_existed…） | 修复采纳链路可审计——「修复率」可统计 |

**缺口**：数据在落库（findings/usage_events/fix_status/评测报告），但没有
运营面把它变成「下一轮更准」的闭环。当前验收口径仍是「报告数量」——数量
不证明质量。Phase 4 的使命：**验收从报告数量转向可证伪指标**。

## 1. 目标与非目标

### 目标

1. **反馈回流**：人工采纳/驳回/修复/回归结果回流到评测集与审查策略
   （采纳→正例候选；驳回→误报判例；修复合并→回归样例）。
2. **维度趋势**：按仓库/语言/模型/插件四维的质量趋势曲线（消费 Phase 1
   口径三分 + Phase 2 Evidence 状态 + Phase 3 修复状态）。
3. **可证伪验收**：每批次能回答「误报率降了吗、有效发现率升了吗」——
   用同口径环比数字说话，不用「报告更多了」。

### 非目标（YAGNI）

- 不做自动调参（模型/提示词自动寻优）——趋势先可见，策略人调；
- 不做实时告警平台（Prometheus 指标面已有 `argus_*`，不重复造）；
- 不做跨实例组织级聚合（=批 E 独立 spec，本阶段只留接口位）；
- 不重建评测基建（evalq/基准集/跑批入口已闭环，只消费）。

## 2. 数据面勘察（现状，实施前复核）

| 数据 | 现存储 | Phase 4 消费 |
|---|---|---|
| 评测报告 | `eval/results/baseline-latest.{json,md}` + archive | 趋势基线（按批次归档对比） |
| 意见+证据状态 | tasks.ResultJSON 内 findings（含 evidence_status） | 有效发现率分子分母 |
| 人工反馈 | 抑制记录（suppress/restore，fp 短码）+ prints 惯犯热力 | 驳回→误报判例（批 E 输入） |
| 修复采纳 | Task.FixStatus（pushed/pr_created/cr_existed…） | 修复率 |
| 逐调用账本 | usage_events 表 | 成本/模型维度 |
| 按仓配置 | repo_settings 表 | 仓库维度分组 |

**实施第一步**：只读勘察复核上表（防过时——与 tech-debt 清扫同纪律）。

## 3. 设计

### 3.1 反馈回流（采纳/驳回/回归）

**回流入口复用既有通道，不新建交互面**：

- **驳回**：`suppress_finding`（fp 短码）已是人工驳回的生产动作——回流=
  抑制记录定期导出为「误报判例候选」（file+kind+comment 指纹），入
  `eval/baseline/` 的 known_fp 扩充池（须人工确认才入金标准，防自动污染）。
- **采纳/修复**：修复 PR 合并（FixStatus 后平台侧 merge 可查）→ 该 finding
  升级为「已修复正例」——回归样例候选（同上须人工确认入基准集）。
- **回流纪律（防污染红线）**：机器只产**候选清单**，金标准变更永远人工
  合入——`eval/baseline/*.yaml` 的 Validate 门禁不动。

**产出物**：`make eval-candidates`（新入口）——扫抑制记录+修复合并记录，
输出 `eval/results/candidates-{fp,regress}.md` 待人工筛选。

### 3.2 维度趋势

**单源**：新建 `internal/qconsole` 趋势投影的扩展（或独立
`internal/qualityops` 包——倾向后者，qconsole 保持 /console 渲染面职责）：

- **输入**：baseline archive（批次序列）+ ResultJSON findings 聚合 +
  usage_events + FixStatus——按 维度键（repo/lang/model/plugin）分组；
- **输出**：JSON 趋势序列（供 /console 渲染与 CLI直读）+ 关键指标环比
  （Precision/Recall/CleanCaseFPRate/Unlabeled 占比/有效发现率/修复率）；
- **有效发现率定义**：`verified+structurally_valid 意见数 / 总意见数`
  （Phase 2 状态直接可用）；**修复率**：`pr_created+cr_existed 对应 PR
  合并数 / 修复尝试数`（Phase 3 状态+平台 merge 查询）。

**口径纪律**：所有环比必须同口径——跨 Phase 1 口径切换点的批次标注
「不可直比」（与 eval-baseline.md 既有声明同源）。

### 3.3 可证伪验收的运营节奏

每批次（或每周）跑一次 `make quality-report`：

```
同口径环比：Precision Δ / CleanCaseFPRate Δ / Unlabeled Δ /
            有效发现率 Δ / 修复率 Δ / 成本（token/有效发现）
```

**红绿判定入档**：任一核心指标环比恶化 >阈值（如 Precision -0.05）→
该批次标黄，须在 execution 清单写归因——「变差也要能解释」。

## 4. 验收（Phase 4）

- [ ] `eval-candidates` 入口落地：误报/回归候选清单可产出（人工合入金标准
      的纪律文档化）；
- [ ] 维度趋势 JSON + /console 或 CLI 消费面（四维分组+关键指标环比）；
- [ ] `make quality-report` 一次跑出同口径环比表（含红绿判定）；
- [ ] 有效发现率/修复率两个新指标有定义文档+至少一个真实批次的数字；
- [ ] 全量门禁绿（lint/test/arch/eval）。

## 5. 风险登记

| 风险 | 缓解 |
|---|---|
| 趋势数据不足（批次少） | 首期只出已有批次序列（archive 里的历史基线），不造数 |
| 维度分组稀疏（单仓单模型） | 分组维度可配置，稀疏维度自动折叠 |
| 修复率依赖平台 merge 查询 | best-effort：查不到 merge 的记 unknown 不计入分母 |
| 与批 E 边界模糊 | 本阶段只产单实例候选清单；跨仓聚合=批 E spec |

## 6. 批 E/F/G 衔接

- **批 E（组织级学习环）**：本阶段的误报判例候选清单是其输入——E 的
  spec 单独出（组织桶写侧聚合+跨仓共享协议）；
- **批 F（需求追溯）/ 批 G（多模态）**：独立 spec，不依赖本阶段；
- 三批 spec 齐后回填 `execution-13items.md` 状态列（既有约定）。
