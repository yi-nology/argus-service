# Argus 批 G 多模态审查设计 spec

- 日期：2026-11-15
- 状态：设计稿（待用户批准后出实施 plan）
- 上游：`docs/execution-13items.md` 批 G（architectural，独立 spec 约定）
- 关联：OCR 通路（ocrshim/delegate，v3.9.0 落地）、visual_check 任务类型

---

## 0. 三面现状勘察

| 面 | 现状 | 判定 | 证据 |
|---|---|---|---|
| OCR 通路 | **已在**：`ocr delegate preview/rule` 确定性子命令（OCR 端零 LLM 零凭证），runner `injectOCRDelegate` dispatch 前注入文件分诊与规则命中；失败=告警降级审查照常 | 可复用 | `internal/runner/inject.go:53-57`、`internal/ocrdeleg` |
| visual_check 任务 | **已在但薄**：`domain.TaskKindVisualCheck` 走同一 R 系列（R4+R5 only），技能词表=`SkillVisualCheck`——与 code_review 共用管线，差异只在技能词 | 半有 | `workflow.go:165-170`、`runner.go:364` |
| 设计稿对照输入 | **无**：没有「设计稿/截图作为审查输入」的结构化通道——visual_check 现在审的是代码 diff 不是图 | 缺 | 勘察实锤 |

**结论**：批 G 不是建新管线，是给 visual_check 补**视觉输入面**——把
「截图/设计稿 vs 代码改动」的对照变成结构化输入进既有 R 系列。

## 1. 目标与非目标

### 目标

1. **视觉输入通道**：任务输入可携带截图/设计稿引用（平台附件 URL 或
   仓内路径），经 OCR/多模态模型转结构化描述，进 R4 审查上下文；
2. **设计走查深化**：`SkillVisualCheck` 词表从「通用审查」深化为
   「UI 变更对照」——断言类（布局/文案/交互态）与截图描述对照；
3. **误报控制先行**：视觉审查最易误报——产出走 Evidence 四态纪律
   （无结构化引用的意见降级/标注），且**首期只 comment 不进 request_changes
   否决面**（视觉判断置信度低，人工终审）。

### 非目标（YAGNI）

- 不做像素级图像 diff（需要截图基建+对齐算法——先走语义对照）；
- 不自建视觉模型（依赖网关多模态能力——**当前 9 模型是否支持视觉
  未实测，实施前必须先探**，探不通就只走 OCR 文本面）；
- 不改 code_review 主链路（本批只动 visual_check 面）；
- 不做设计稿管理系统（输入=引用，不上传不存储图资产）。

## 2. 设计

### 2.1 视觉输入通道

```
任务输入（visual_check）：
  diff（既有）+ visual_refs: [{kind: screenshot|design, ref: URL|仓内路径}]
       ↓
  预处理（dispatch 前，与 injectOCRDelegate 同层）：
    仓内路径 → workcopy 读文件 → OCR delegate（已有通路）→ 文本
    URL → 拉取（best-effort，超时降级）→ OCR/多模态 → 文本
       ↓
  产出 ArtifactVisualBrief（结构化视觉简报）挂 task Artifacts
       ↓
  R4 分发（SkillVisualCheck 词表消费简报）→ R5 裁决（首期 comment-only）
```

- **输入形态**：webhook/TaskAPI 的 visual_check 任务带 `visual_refs`
  （可选，无=退化为现行为零破坏）；
- **预处理失败=降级**（与 OCR delegate 同纪律：告警+审查照常，不阻断）；
- **多模态依赖**：简报生成优先走 OCR 文本面（已有）；若网关有视觉模型
  （实施前实测），追加「图→结构化描述」增强——**两条路都落
  ArtifactVisualBrief 同一形态**，消费者不感知来源。

### 2.2 SkillVisualCheck 深化

词表从通用改为 UI 对照断言族（示例）：
- `ui-layout-mismatch`（布局与设计稿不符）
- `ui-copy-mismatch`（文案不一致）
- `ui-state-missing`（交互态缺失——loading/empty/error）
- `ui-a11y-gap`（可访问性缺口）

协议纪律同 code_review：闭集词表+Evidence 引用（file:line 或截图区域
描述）+R5 真值表（首期 **high 也只 comment**——见 2.3）。

### 2.3 误报控制（视觉审查的特殊纪律）

1. **首期 comment-only**：visual_check 的 verdict 上限=comment——视觉
   判断不进 request_changes 否决面（与 evidence 降级不同，这是**任务类型
   级**的保守，写入 R5 对 visual_check 的分支）；
2. **Evidence 四态复用**：视觉意见也走 Phase 2 核验（截图描述类引用
   file:line 或 visual_ref 区域——无引用=unverified 只标不降，与 Ruling②
   一致）；
3. **评测基建**：视觉审查的基准集（截图+预期意见）**不存在**——首期
   不进 evalq 主口径（视觉样例形态不同），单列 `eval/visual/` 留位，
   有样例后再接入。

## 3. 验收（批 G）

- [ ] `visual_refs` 输入通道（webhook/TaskAPI 可选字段，无=零破坏退化）；
- [ ] 预处理落 `ArtifactVisualBrief`（OCR 路径实弹+失败降级纪律）；
- [ ] SkillVisualCheck 闭集词表深化+协议句（四态核验同源）；
- [ ] R5 visual_check comment-only 分支（首期保守）；
- [ ] 多模态增强探针（网关视觉能力实测——**结果写入本 spec 状态头**，
      探不通=OCR-only 如实记录）；
- [ ] 全量门禁绿（lint/test/arch/eval）；视觉基准集留位不接主口径。

## 4. 风险登记

| 风险 | 缓解 |
|---|---|
| 网关无视觉模型 | OCR-only 降级路径完整（简报=OCR 文本），增强是增益不是依赖 |
| 视觉误报率高 | comment-only+Evidence 四态+基准集留位（有评测才放开否决面） |
| 截图拉取失败/超时 | best-effort 降级（与 OCR delegate 同纪律），不阻断 |
| visual_ref 输入滥用（大图轰炸） | 数量/尺寸上限（配置项），超限截断+告警 |

## 5. 边界

- **Phase 4**：visual_check 的意见进维度趋势（plugin 维度已有位），但
  视觉指标单列（不与代码审查口径混比）；
- **批 E/F**：无耦合；
- **43 现场**：真实截图/设计稿样例的实弹验收（回公司后——含网关视觉
  能力实测）。
