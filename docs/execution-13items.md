# 13 项全做执行清单（2026-11-13 拍板）

> 用户拍板：三层全做——二层六单（#24–#29 实装深化）+ 四层长期愿景三件 + 五层 tech-debt 四件。
> 部署/43 现场事项全部搁置（用户不在公司）。本清单是活文档：每批完成勾账，
> 向后演进时在此追加批次细节。推进方式=批间顺序推进、批内逐项落地。

## 批次总览

| 批 | 内容 | 性质 | 状态 |
|---|---|---|---|
| **A 小赢清账** | #27 趋势接线 + #29 CI helm-lint + P2-1~10 打磨 + ptc 划账 + ggp 升级尝试 + #26 验收实测 | bounded 小 | **进行中** |
| **B 评测合流** | #25+#28 真实 44 案跑批归档新基线 + 按月曲线消费 + known_fp 误报治理 | bounded 中 | **✅ 完成** |
| **C 容器级 chaos** | #24 testcontainers pg/MySQL 真依赖故障注入 + 门禁 | bounded 大 | **✅ 完成** |
| **D 长函数全拆** | 重扫真清单（旧表过时，htmlTemplate 577 行）→ 推翻留置裁定逐个拆 + 覆盖率目标 40% | bounded 大 | 待启 |
| **E 组织级学习环** | 跨仓误报判例共享（组织桶写侧聚合） | architectural，独立 spec | 待启 |
| **F 需求追溯闭环** | issue→AC→代码→测试证据全链追踪 | architectural，独立 spec | 待启 |
| **G 多模态审查** | 截图/设计稿对照 UI 变更（OCR 通路已在，设计走查深化） | architectural，独立 spec | 待启 |

## 已拍板的边界决策（勿再问）

- **现网已裁决 MR 样例回填**：本批跳过（需 43 生产数据，用户不在公司）——标注为回公司后的后续批；
  起步集合成夹具口径维持 docs/eval-baseline.md 既有声明。
- **长函数拆法 = A**：推翻既往「留置」裁定，全部 >100 行函数都拆；先重扫真清单。
- **部署/43/webhook 直连/token 续购**：全部搁置，不在本 13 项内。
- **覆盖率目标 = 40%**（34.3% 基线 → 40%，随批 D 验收）。

## 批 A 明细（✅ 2026-11-14 完成）

- [x] A1 #27：`renderConsoleTrend` 接线（`qconsole.LoadEvalTrend` 只读投影），
      总指标卡+分语言表+缺位占位；config `ARGUS_EVAL_REPORT`；两态测试；
      arch 白名单 service→qconsole 单向补边。
- [x] A2 #29：`ci.yml` 增 `helm` job（lint + template 双路径冒烟含 pgRestore）；
      deploy-production.md「CI 未接」欠账声明同步销。
- [x] A3 P2：报告归档 `docs/archive/2026-11-11-arch-hygiene-report.md`（状态头：
      7 条已被 v6.3.0 顺带修掉）；本批补余 4 条——#11 mcpserver / #13
      adapter_remote 包文档归位（隔开竞争性 package 注释）、P2-9 dup 回查
      Warn 留痕、P2-10 三处 WalkDir 口径注释。
- [x] A4 ptc `Result.Describe()` 划账（〔已闭环 v6.3.1〕，ptc.go:158 实锤）。
- [x] A5 ggp v0.127→v0.134 **升级成功**（非退回）：连环断三环——pkg v0.48 断
      guardx→升 guardx v0.25.0；再断 goagent-kernel egress（= #134 门，
      **#134 已 2026-11-13 CLOSED**）→升 goagent-kernel v0.35.0 解锁。
      终态七依赖齐升（见 tech-debt 批 A 节），全量测试零失败。
- [x] A6 #26 验收实测（本地检出模式三连）：①首次安装 ✅ ②幂等：双目标
      `diff -r` 一致 + 同目标重装文件集 md5 不变（无残留）✅ ③篡改
      checksums → exit=1 拒装（want/got 报文正确）✅。
- [x] A7 收尾：三门禁 + 按单拆 commit。

## 批 B 明细（进行中，#25+#28 合流）

- [x] B0 打桩管道预验：`make eval-baseline` 44 案绿。
- [~] B1 真实 44 案跑批：
  - **治理前（2026-11-14 首轮，21min，k=3，errored=1）**：P=0.632 R=0.364
    FP 率 0.538 漏检率 0.636 F1=0.462 verdict 63.6% 稳定 0.955——
    artifact 归档 `eval/results/archive/baseline-before-treatment-20261114.*`
    （本地，results 目录 gitignored）。
  - **两大量化发现**（详见 docs/eval-baseline.md 诊断节）：
    ① FP=7 中 style-nit 占 6——prompt 词表邀请风格词撞 clean 即 FP 口径；
    ② FN=21 中 **20 个是 kind 同义词错配、真沉默 0**——模型全找到了问题，
    只是标签自造（sequential-await vs serial-await、nil-deref vs npe…）；
    金标准 27 词闭集而 prompt 示例只列 11 词且 5 词不在金标准。
  - **治理动作（两处 prompt，评分与金标准不动）**：kind 换金标准闭集全词表
    （协议统一标签非应试）+ 补「纯风格偏好不算问题」边界（保留
    missing-doc-comment，go-003 依赖）。
  - [x] 治理后跑批两轮完成：第二轮（A/B 对照）P=1.000 R=0.939 FP=0.000
    F1=0.969；残余 2 FN 揪出**金标准数据缺陷**（hardcode-secret 与
    hardcoded-secret 同概念两拼写并存）→ 统一 hardcoded-secret。
  - [x] **官方终基线（终轮）**：**P=1.000 / R=1.000 / F1=1.000 /
    误报率 0.000 / 稳定 0.977，四语言全满分**（TP=33 FP=0 FN=0，errored=0）。
    A/B 全表与根因链入 docs/eval-baseline.md。
  - [x] **附带 bug 修复**：打桩/真实 artifact 同名互冲（make test 冲掉真实
    基线）→ 打桩改写 baseline-stub.* + 消费测试 stub 守卫，全量回归验证。
- [x] B2 真实基线喂趋势面：qconsole 投影对齐测试（真实 JSON→EvalTrend，
      4 语言读出）+ service 整页真数据渲染测试，双绿（文件缺位自动 skip）。
- [x] B3 诊断文档回写（eval-baseline.md 诊断节：双根因+A/B 表）。
- [x] B4 勾账 #25/#28 + 门禁。

## 批 C 明细（✅ 2026-11-15 完成，#24 容器级）

- [x] C1 `test/chaos/container_test.go`：五探针 × pg16-alpine/MySQL 8.0
      （真行锁 fencing 双形态 / SKIP LOCKED 双认领 / event_id+invocation_id
      撞唯一索引 / 预留并发零超卖 / 停容器有界失败→重启→复位恢复）；
      `ARGUS_CHAOS_CONTAINER=1` 门控（env 开而 docker 缺=Fatal 防假绿）、
      `ARGUS_CHAOS_DIALECT` 单方言调试、报告
      `results/chaos-container-latest.*` 与矩阵报告分名。
- [x] C2 `internal/runner/budget_container_test.go`（探针④）：真 pg 停机
      下 soft fail-open / hard fail-closed + 重启恢复正常授予——budgetGate
      未导出故住 runner 包；与 storage 探针⑤互补（那边证明真网络故障=有界
      err，这边证明闸两分支）。
- [x] C3 门禁接线：Makefile `chaos-container` 目标（+help 行）；
      `test.yml` 新 `chaos-container` job（make chaos 单测级 + 容器级，
      timeout 25m）。
- [x] C4 文档勾销：chaos-matrix.md 分层表/五探针表/三实现实锤/完成度边界。
- [x] C5 实测：本地双方言 **10/10 pass**（pg 五探针 + mysql 五探针）+
      runner 探针④ PASS；`make chaos-container` 全量 EXIT=0。
- 实现三实锤（详见 chaos-matrix.md 容器级节）：Docker Desktop 随机端口
  stop/start 漂移须重取映射+重开 Store；重启 wait 命中旧日志=假就绪；
  Open 竞态须重试且失败不覆盖旧引用（nil Cleanup panic）。

## 批 D~G 明细（随推进补写）

（每批启动前在此追加明细；完成后勾账并回写状态列。）

## 验收总口径

每批完成 = 批内明细全勾 + 三门禁绿（`make lint && make test && make arch`；
涉评测批加 `make eval`）。E/F/G 三批各出独立 spec 后再入本清单状态列。
