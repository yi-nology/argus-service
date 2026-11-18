// tenant 包 —— 多租户贯穿维度基元（#80 母票地基，Phase A）。
//
// 设计裁定（2026-11-18）：共享库 + tenant_id 贯穿维度。存量行与未声明租户的
// token/内部路径全部归入 Default 租户（"default"）——现有单租户部署行为零变化。
// 租户解析统一走 context：鉴权面（Bearer token 的 tenant 声明）/webhook 面
// （tenant_repos 映射）/内部路径（无 ctx 声明 = Default）三种来源都在进入
// 存储层之前把租户写入 ctx，storage 用 Scoped scope 强制拼 tenant_id 谓词，
// 从机制上消灭「忘加过滤」的新端点泄露面（#94）。
//
// 包纪律：本包是叶子基元，只依赖 gorm（scope 需要）与 stdlib，不 import 任何
// internal 包——任何层都可以安全引用。
package tenant
