// merge.thrift —— 受控合并域（T2 合并资格门）：preview→confirm 两阶段远端写。
// 统一资格门（fail-closed）：PR 开放 + head 一致 + mergeable 已知 + 当前 head
// 审查完成 + 无未处理 critical/high 发现 + CI 通过（无 CI/读取失败 = 阻断）。
// 类型本域定义；service 声明在 master idl/argus.thrift；model 生成
// biz/model/argus/merge.go。全部端点 admin 档（requiredScope + 方法内二次判定）。
namespace go argus

// MergeGateCheck 单条资格判定（ok=false 时 code 为稳定机器码）。
struct MergeGateCheck {
    1: string code               // cr_open / not_draft / head_match / mergeable / review_done / no_blocking_findings / ci_pass
    2: bool ok
    3: string message            // 人读说明（缺失字段如实写明，不编造）
}

// MergeEligibility 统一资格门的完整判定投影。
struct MergeEligibility {
    1: bool eligible
    2: string head_sha           // 判定时的远端 head（空 = 未取到）
    3: string merge_status       // 平台 merge_status 原文
    4: string review_task_id     // 当前 head 的审查任务（空 = 无）
    5: string review_verdict     // approve / comment / request_changes（空 = 未完成）
    6: string ci_status          // success / failure / pending / unknown（unknown = fail-closed 阻断）
    7: i32 blocking_findings     // 未处理 critical/high 条数
    8: list<MergeGateCheck> checks
}

// MergeEligibilityRequest 只读资格查询（不建操作、不写远端）。
struct MergeEligibilityRequest {
    1: required string platform
    2: required string owner
    3: required string repo
    4: required string number
}

struct MergeEligibilityResponse {
    1: MergeEligibility eligibility
}

// MergePreviewRequest 创建合并候选（绑定 expected_remote_head，不执行合并）。
struct MergePreviewRequest {
    1: required string platform
    2: required string owner
    3: required string repo
    4: required string number
}

struct MergePreviewResponse {
    1: string op_id                     // 候选操作 ID（confirm 用）
    2: bool eligible                    // 预检是否通过（false = 不建 op）
    3: string expected_remote_head      // 绑定的远端 head
    4: MergeEligibility eligibility
    5: string reject_code               // eligible=false 时的首条失败机器码
}

// MergeConfirmRequest 确认合并：绑定 op_id + expected_remote_head（与远端当前
// head 不一致则拒绝；陈旧证据不可放行）。执行经副作用闸 at-most-once，
// 超时/断连 → uncertain + 读回对账。
struct MergeConfirmRequest {
    1: required string op_id
    2: required string expected_remote_head
}

struct MergeConfirmResponse {
    1: string op_id
    2: string status                    // merged / uncertain / failed / rejected
    3: optional string message
    4: optional string merged_sha       // 平台返回的合并结果标识
}

// MergeOperation 合并操作投影（GET /merges/:opId）。
struct MergeOperation {
    1: string op_id
    2: string platform
    3: string owner
    4: string repo
    5: string number
    6: string status                    // pending / merged / failed / uncertain / expired
    7: string expected_remote_head
    8: optional string reason
    9: optional string result_json
    10: i64 created_at
    11: i64 updated_at
}

struct MergeGetResponse {
    1: MergeOperation op
}

// MergeReconcileRequest 读回对账 uncertain 操作（按平台实际状态幂等定案）。
struct MergeReconcileRequest {
    1: required string op_id
}

struct MergeReconcileResponse {
    1: string status                    // 定案后的状态
    2: optional string message
}
