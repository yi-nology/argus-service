// fix_approval.thrift —— 高危修复审批票（T3）：critical/high 意见具名批准门。
// 类型本域定义；service 声明在 master idl/argus.thrift。admin 档。
namespace go argus

// FixApprovalTicket 一张审批票投影。
struct FixApprovalTicket {
    1: string id
    2: string platform
    3: string owner
    4: string repo
    5: string number
    6: string head_sha
    7: string print                 // fp 指纹
    8: string file
    9: string severity              // critical / high
    10: string status               // 状态：待裁决 / 已批准 / 已拒绝 / 已陈旧（词表见 storage.FixApproval*）
    11: optional string reason
    12: optional string decided_by_fp
    13: i64 created_at
    14: optional i64 decided_at
}

// FixApprovalListRequest 按 PR 列票（head 可选，缺省当前远端 head 的票+历史）。
struct FixApprovalListRequest {
    1: required string platform
    2: required string owner
    3: required string repo
    4: required string number
    5: optional string head_sha
}

struct FixApprovalListResponse {
    1: list<FixApprovalTicket> tickets
    2: i64 pending_count                 // 当前 head 待裁决票数
    3: optional string head_sha    // 实际使用的 head
}

// FixApprovalDecideRequest 裁决一张待裁决票。
struct FixApprovalDecideRequest {
    1: required string id
    2: required string decision      // 批准 | 拒绝（storage.FixApprovalApproved|Rejected）
    3: optional string reason
}

struct FixApprovalDecideResponse {
    1: string id
    2: string status                // 裁决后状态
    3: bool changed                 // false = 非待裁决态（幂等拒绝）
    4: optional string message
}
