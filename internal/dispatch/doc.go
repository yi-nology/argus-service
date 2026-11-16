// Package dispatch 审查引擎面（v3.173.0 自 plugin 拆出）：插件分发执行面
// （Dispatcher fan-out/预算/熔断，dispatcher*.go）、结果聚合（Merger 去重/
// 抑制 + best-of-N 采样共识，merger.go/bestofn.go）、编队共识（squad_
// consensus.go：多 agent 共识黑板跨轮合并）与注册表（registry.go）；契约/
// 观测直引 plugin/domain（v7.1.0 别名桥拆除），方向单一。
// 原则：路由/裁决/合并不走 LLM，全部确定性 Go 代码；N 采样的模型自评 verdict
// 不参与裁决，复现计数不进指纹（domain.Finding.Samples 结构化标注，渲染层展示）。
//
// 与 kernel dispatch 同名不同义（#41）：agent-harness 的 dispatch（核心下沉在
// oagentkit/agent/dispatch）是「权限即拓扑」的派发守卫门面——agent 间可达性由
// allow 矩阵物理决定，拒绝返回结构化 DenyError；本包不 import 它、与其零共享
// 语义。本包的 dispatch 是审查编排领域词：把一个评审任务分发给多个专家插件
// （fan-out/预算/熔断）、聚合多路结果并做共识裁决。读者按「kernel=守卫门面、
// argus=领域编排」区分；重命名为 internal/squad 等领域名的方案 import churn
// 高，另立项处理（ARCHITECTURE.md 有同款标注）。
package dispatch
