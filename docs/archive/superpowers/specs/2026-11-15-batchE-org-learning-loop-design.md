# Argus 批 E 组织级学习环设计 spec

- 日期：2026-11-15
- 状态：设计稿（待用户批准后出实施 plan）
- 上游：`docs/execution-13items.md` 批 E（architectural，独立 spec 约定）
- 关联：Phase 4 spec §3.1（单实例候选清单=本批输入）、v5.8.2 惯犯热力（prints 跨 MR 史）

---

## 0. 背景：先把「组织级」的语义钉准

13 项清单对批 E 的原话是「跨仓误报判例共享（组织桶写侧聚合）」。勘察后
必须先消歧——**当前部署形态下有两层语义**：

| 层 | 现状 | 结论 |
|---|---|---|
| 同实例多仓 | argus 单实例（43 k3s/PostgreSQL）接全部仓 webhook——`finding_prints` 表已天然多仓共存 | **已共享存储，但指纹不共享**：唯一键含 `repo+number`（`internal/storage/models.go:88-94` idx_print 五段），同一误报模式在仓 A 被 suppress 不影响仓 B |
| 跨实例 | 多套 argus 部署（未来组织多团队各自部署） | 无任何共享通道——**真·组织级缺口** |

**本批钉的语义**：以「**同实例内跨仓判例共享**」为一期主目标（写侧聚合
按组织/模式桶），「跨实例共享」为二期留接口位不实施——跨实例要新基础设施
（共享存储/同步协议），43 现场只有一个实例，无实证需求前不建（YAGNI）。

## 1. 目标与非目标

### 目标

1. **误报判例桶**：把单仓 suppress 信号聚合成「跨仓可复用的判例」——
   同类误报模式（file 形态+kind+comment 指纹的**仓无关投影**）在桶内共享，
   新仓命中桶判例时降权/预 suppress（可配置，缺省只提示不自动 suppress）。
2. **写侧聚合**：suppress 动作（人工驳回）自动产判例候选，经确认入桶——
   与 Phase 4 §3.1「机器产候选、人工合入」纪律同源。
3. **可审计**：每条桶判例带来源链（哪个仓哪次 suppress），被消费（降权/
   提示）时留痕。

### 非目标（YAGNI）

- 跨实例同步协议/共享服务（二期，等多实例实证）；
- 自动 suppress（误伤成本高——桶判例缺省只提示+降权参考，人工确认才抑制，
  与 prints 热力「抑制计数=误报降权参考」既有语义一致）；
- 机器学习聚类（指纹投影=确定性规则，不做向量相似度）。

## 2. 现状勘察

| 面 | 现状 | 证据 |
|---|---|---|
| 抑制存储 | `finding_prints` 表：指纹五段（platform/owner/repo/number/print）+File/Line/Severity/Comment/Status/Plugin/TimesSeen/RegretCount | `internal/storage/models.go:88-115` |
| suppress 通道 | `suppress_finding`（fp 短码）/`restore_finding` 反馈通道已闭环 | `idl/findings.thrift` FindingFeedbackService |
| 误报历史 | RegretCount（R3.5 反思剔除累计）+ TimesSeen——单仓内已有「惯犯」信号 | 同上 |
| 热力消费 | prints 跨 MR 意见史（total≥3 入块，抑制计数=降权参考） | v5.8.2 |
| 组织维度 | **无 org 字段**——Owner 是最接近的组织投影（GitLab group/GitHub org） | 勘察实锤 |
| 单实例候选清单 | Phase 4 `eval-candidates`（设计中）——本批的上游输入 | Phase 4 spec §3.1 |

## 3. 设计

### 3.1 判例桶（新表 `fp_case_buckets`）

```go
// 判例桶：仓无关投影的误报模式（写侧聚合产物）。
type FPCaseBucket struct {
    ID          uint   // PK
    Pattern     string `gorm:"uniqueIndex"` // 仓无关指纹投影（见 3.2）
    Kind        string // 归一类目（file 形态归一后的类别，如 "hardcoded-secret"）
    Org         string // 聚合轴：Owner（GitLab group/GitHub org）——分桶不跨 org
    SampleRepo  string // 首次来源仓（审计锚）
    SamplePrint string // 样例 fp 短码（回查原始意见）
    HitCount    int    // 被消费次数（提示/降权命中）
    Status      string // candidate | confirmed | rejected（人工确认态）
    // ...审计时间戳
}
```

**红线**：桶只存**模式与审计锚**，不复制意见全文（PII/体积）；确认态
（confirmed）才参与消费，candidate 只出现在 Phase 4 候选清单里。

### 3.2 仓无关指纹投影（Pattern 构造）

原指纹含 `repo+number`（仓内唯一定位）——投影规则（确定性、可测试）：

```
Pattern = hash(platform + "|" + org + "|" + 归一(file) + "|" + 归一(comment))
归一(file)  = 去目录层级的文件名 + 扩展名（auth/login.go → login.go；按配置可全路径）
归一(comment)= 去数字/引号/标识符字面量后的骨架（"口令硬编码在 config.go" → "口令硬编码在 <file>"）
```

- **org 轴不跨**：GitLab group 间不共享判例（不同组织的信任边界不同）；
- 投影函数进 `internal/domain`（纯函数）+ 表驱动测试（同模式跨仓同 Pattern
  / 不同模式不同 Pattern / 归一不误伤）。

### 3.3 写侧聚合与消费

- **写**：suppress_finding 成功后（同实例信号），异步 upsert 桶——
  Status=candidate；已有 candidate/confirmed 则 HitCount++。
- **读（消费）**：审查管线渲染面（findings 侧栏/报告）在 prints 热力旁
  增「组织判例命中」提示——**只提示不抑制**（3.1 红线）；confirmed 判例
  命中时提示更强措辞（「本组织已确认同类误报 N 次」）。
- **人工确认**：复用 Phase 4 候选清单机制——`make eval-candidates` 顺带
  输出桶 candidate 待审清单；确认动作走 admin API（新端点，RBAC 同
  repo-settings 面）。

### 3.4 二期（跨实例）留接口位

- Pattern 构造函数与桶表结构即共享协议的种子（跨实例同步=同步桶行）；
- 二期触发条件（写入本 spec 不实施）：第二个 argus 实例上线且有共享诉求。

## 4. 验收（批 E）

- [ ] `fp_case_buckets` 表+仓无关投影函数（纯函数+表驱动测试：跨仓同模式
      同 Pattern/不同 org 不共享/归一不误伤）；
- [ ] suppress→candidate 聚合接线（异步、失败不阻断 suppress 主链路）；
- [ ] 审查面「组织判例命中」提示（candidate 弱提示/confirmed 强提示，
      **不自动抑制**）；
- [ ] admin 确认端点（candidate→confirmed/rejected，RBAC 同 repo-settings）；
- [ ] `eval-candidates` 输出桶待审清单（与 Phase 4 机制合流）；
- [ ] 全量门禁绿（lint/test/arch/eval）。

## 5. 风险登记

| 风险 | 缓解 |
|---|---|
| 投影过粗误共享（不同仓同名文件误判同模式） | org 轴隔离+comment 骨架参与 hash+candidate 缺省不抑制 |
| 投影过细不共享（归一不足） | 归一规则表驱动可调，首批宁细勿粗（共享是增益不是正确性依赖） |
| 桶表膨胀 | Pattern 唯一键天然去重；HitCount=0 且超期 candidate 可清理（留 TTL 策略） |
| 与 suppress 主链路耦合 | 聚合异步 best-effort（GoSafe 收编纪律），失败只留痕 |

## 6. 边界

- **Phase 4**：其 §3.1 单实例候选清单是本批写侧输入；本批桶消费提示不改
  Phase 4 指标口径；
- **批 F/G**：无耦合；
- **43 现场**：表迁移随 AutoMigrate；确认端点部署后实弹（回公司后验收项）。
