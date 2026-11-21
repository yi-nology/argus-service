// fix_push.thrift —— 修复推送用户面 preview→confirm（T4，ARGUS_FIX_PUSH=confirm）。
// 验证+审批后落 await_confirm；操作员绑定 expected_remote_head 后放行。
namespace go argus

// FixPushPreviewRequest 只读预览：最近任务的推送就绪态（不执行任何远端写）。
struct FixPushPreviewRequest {
    1: required string platform
    2: required string owner
    3: required string repo
    4: required string number
}

struct FixPushPreviewResponse {
    1: optional string task_id
    2: optional string head_sha
    3: optional string fix_status      // await_confirm / pushed / … / (无任务)
    4: bool ready                     // await_confirm 且审批齐备
    5: string message
    6: i32 pending_approvals          // 当前 head 未批准高危票数
    7: optional string mode           // FixPushMode（branch/direct/confirm）
}

// FixPushConfirmRequest 确认推送：绑定 task_id + expected_remote_head。
struct FixPushConfirmRequest {
    1: required string task_id
    2: required string expected_remote_head
}

struct FixPushConfirmResponse {
    1: string task_id
    2: string status                  // pushed / rejected / failed
    3: string message
    4: optional string head_sha
    5: optional string fix_status
}
