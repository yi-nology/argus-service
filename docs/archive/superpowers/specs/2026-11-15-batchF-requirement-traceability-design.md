# Argus 批 F 需求追溯闭环设计 spec

- 日期：2026-11-15
- 状态：设计稿（待用户批准后出实施 plan）
- 上游：`docs/execution-13items.md` 批 F（architectural，独立 spec 约定）
- 关联：Phase 2 Evidence 四态（证据面同族）、requirement 工作流（R1-R5）

---

## 0. 目标：issue→AC→代码→测试 四环成链

批 F 原话「issue→AC→代码→测试证据全链追踪」。先把四环现状钉准——**不是
每环都要新建，是把已有半环接起来**。

## 1. 四环现状勘察（实施前复核）

| 环 | 现状 | 判定 | 证据 |
|---|---|---|---|
| ① issue→AC | **半有**：R1 从任务输入提取 `RequirementSpec{ACs []AcceptanceCriterion}`（spec 文本→AC 列表），但 AC 与上游 issue 编号无锚——spec 从哪来（issue 正文？人工粘贴？）无结构化回链 | 缺锚 | `internal/plugin/types.go:217-227`、`internal/workflow/requirement/r1.go:77,120` |
| ② AC→代码 | **半有**：ACCheck 四态（+undeclared/inconsistent 两态——「描述没提但对外语义变了」）判定 AC 与代码的对应；completeness 裁判 AC 漏判 | 已有判定，缺持久锚 | `internal/plugin/findingswire.go:35`、`completeness.go:31` |
| ③ 代码→测试 | **弱**：审查面报「缺测试」类意见，但「AC X由测试 T 覆盖」的正向映射不存在 | 缺正向链 | 现状=审查意见单向 |
| ④ 测试→证据 | **半有**：Phase 2 Evidence 四态（意见级）+ Claim 级 evidence——但那是**审查证据**不是**测试证据**（测试通过记录/断言清单未入链） | 族同源，面不同 | `internal/plugin/evidence.go`（Phase 2） |

**结论**：四环里 ①② 是「锚缺失」（数据有、链断），③④ 是「面缺失」（要
新建的最小面）。**不建追踪平台**——链的载体=既有 task/artifact 体系。

## 2. 目标与非目标

### 目标

1. **AC 锚**：RequirementSpec 落 `source_ref`（issue URL/编号，可选字段）
   ——R1 提取时若任务输入带 issue 引用则锚上，产物落 task Artifacts；
2. **追溯链载荷**：每条 AC 产出 `trace` 结构（AC→涉及文件/符号→关联测试
   文件→状态），挂 task Artifacts（`artifact_trace_chain`），报告面渲染
   「追溯链」段；
3. **断链检测**：AC 无代码映射（undeclared）/代码改动无 AC 归属（scope
   creep 已有）/AC 无测试覆盖——三类断链进 completeness 或独立报告段，
   **只报告不阻断**（首期）；
4. **测试证据**：测试文件清单（changed 中 `*_test.go`/tests 目录）与 AC
   的启发式映射（同文件名/同符号名）——启发式明确标注「推断」，不冒充
   证明。

### 非目标（YAGNI）

- 不做需求管理工具（不建 issue 写回/不反向同步）；
- 不做精确测试覆盖分析（需要跑测试收集覆盖率——依赖执行面，留后续）；
- 不改 R1-R5 裁决语义（追溯是**报告增强**，不进否决面——与 evidence
  降级不同，首期零裁决影响）；
- 不建独立追溯数据库（ArtifactsJSON 承载，与既有 stage_artifacts 同族）。

## 3. 设计

### 3.1 数据形态（挂在既有体系上）

```go
// plugin 包（契约面）
type ACTrace struct {
    ACID        string   `json:"ac_id"`         // AC 编号（spec 内）
    SourceRef   string   `json:"source_ref,omitempty"` // issue 锚（①环）
    Files       []string `json:"files,omitempty"`       // 代码映射（②环，启发式/模型产出）
    Tests       []string `json:"tests,omitempty"`       // 测试映射（③环，启发式）
    TestStatus  string   `json:"test_status,omitempty"` // covered | inferred | none
    CodeStatus  string   `json:"code_status,omitempty"` // implemented | undeclared | inconsistent
    EvidenceRef string   `json:"evidence_ref,omitempty"` // ④环：指向测试证据/审查 evidence
}
```

- 落 `task.Artifacts[ArtifactTraceChain]`（JSON 序列化，幂等覆盖写）；
- **产出点**：finish 步骤（与 verifyFindingsEvidence 同层）——用已有
  ACCheck 结果+changed 文件+模型补充（R4 结果里若带 AC→文件映射则消费，
  没有则纯启发式）；
- **渲染点**：报告「追溯链」段（reporttmpl 白名单新段）+ MCP 出口
  （findingDTO 同族——trace 链单独 DTO）。

### 3.2 断链检测（三类，只报告）

| 断链 | 检测 | 去处 |
|---|---|---|
| AC 无代码映射 | CodeStatus=undeclared（ACCheck 已有态） | completeness 备注/追溯段 |
| 改动无 AC 归属 | 已有 scope creep 判定 | 复用，不重复建 |
| AC 无测试覆盖 | Tests 空且 changed 有同名测试文件缺失 | 追溯段标 `test_status=none` |

### 3.3 与 Evidence 四态的关系（边界钉死）

- Evidence 四态=**单条意见的引用可信度**（Phase 2）；
- ACTrace=**需求条目的覆盖链**（本批）；
- 两者在④环交汇（trace.EvidenceRef 可指向审查 evidence），但**不合并
  字段**——语义不同面（意见级 vs 需求级），合并会重蹈「纯文字冒充
  verified」的口径混乱。

## 4. 验收（批 F）

- [ ] `ACTrace` 结构+`source_ref` 锚（R1 提取接线，可选字段零破坏）；
- [ ] finish 步骤产出 trace 链（挂 Artifacts，幂等）；
- [ ] 报告「追溯链」段渲染（含三类断链标注）+ MCP trace 出口；
- [ ] 测试映射启发式带「推断」标注（不冒充证明）；
- [ ] 零裁决影响（追溯不进 R5 否决面，回归=golden eval 不变）；
- [ ] 全量门禁绿（lint/test/arch/eval）。

## 5. 风险登记

| 风险 | 缓解 |
|---|---|
| 启发式映射误报（同名文件不同物） | 明标 inferred 不冒充；宁缺勿假（映射不确信就留空） |
| 模型产出 AC→文件映射不稳定 | 首期纯启发式，模型面等有实证再接（YAGNI） |
| 报告段膨胀 | 追溯段默认精简（断链优先展示），全链可折叠 |
| issue 锚依赖输入格式 | source_ref 可选——没有就空，不阻断 R1 |

## 6. 边界

- **Phase 4**：trace 的 test_status/断链计数可入维度趋势（质量指标族），
  但本批不接（先有数据再上指标）；
- **批 E/G**：无耦合；
- **43 现场**：真实 issue→AC→代码→测试链路的验收需现场样例（回公司后
  实弹项）。
