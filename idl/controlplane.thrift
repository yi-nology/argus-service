// controlplane.thrift —— 控制面人工介入域（#48）：操作员处置未知副作用/资源租约/
// 在飞任务。类型全部本域定义——**不引入 kernel controlplane 类型**（Plane 绑
// taskboard/tenant，宿主无法用自己的任务 ID/租户类型；只取模式）。
// 共享 namespace go argus；service 声明在 master idl/argus.thrift；model 生成
// biz/model/argus/controlplane.go。所有端点均 admin 档（requiredScope 前缀收口
// + service 层 ScopeFromContext 二次判定）。
namespace go argus

// ControlPlaneEffect 一条 uncertain 副作用（GET /controlplane/side-effects 行）。
struct ControlPlaneEffect {
    1: string key           // sideeffect 幂等键
    2: string resource      // 资源身份（评论/PR/分支等）
    3: string reason        // 转入未知态的原因
    4: i32 retry_safety     // 0=safe / 1=unknown / 2=unsafe（kernel 词表）
    5: i64 updated_at       // Unix 秒
}

struct ControlPlaneListEffectsResponse {
    1: list<ControlPlaneEffect> effects
}

// ControlPlaneResolveRequest 人工裁决一条 unknown 副作用。
struct ControlPlaneResolveRequest {
    1: string key                   // 必填，≤255
    2: bool applied                 // true=确认已生效；false=确认未生效
    3: optional string payload      // applied=true 时的结果 JSON；评论型给 {"comment_id":N}
    4: bool force_retryable         // applied=false 时：Unsafe 动作必须显式签字
    5: optional string reason       // 裁决依据（审计留痕）
}

struct ControlPlaneResolveResponse {
    1: string outcome               // replayed / applied / uncertain
    2: optional string payload      // applied=true 时回填的事件结果
}

// ControlPlaneLease 一条活跃 side-effect 资源租约。
struct ControlPlaneLease {
    1: string resource
    2: string holder                // 持有者身份（侧效闸铸造，非 token）
    3: i64 fence                    // 已发放的最大栅栏令牌
    4: i64 expires_at               // Unix 秒
}

struct ControlPlaneListLeasesResponse {
    1: list<ControlPlaneLease> leases
}

// ControlPlaneReleaseAction 强释预估清单里的一行（dry_run=true 的只读投影）。
struct ControlPlaneReleaseAction {
    1: string key                   // sideeffect 幂等键
    2: string resource              // 资源身份
    3: optional string holder       // 执行意图持有者（空 = 仅对账占用）
    4: optional i64 fence           // 执行意图令牌
    5: string status                // pending / uncertain（done 行不受强释影响）
    6: i32 retry_safety             // 0=safe / 1=unknown / 2=unsafe（kernel 词表）
    7: string reason                // 当前失败理由（转入 uncertain 时会追加强释留痕）
    8: i64 updated_at               // Unix 秒
}

// ControlPlaneReleasePlan 强释预估（dry_run=true 返回）：正式释放将影响的一切。
struct ControlPlaneReleasePlan {
    1: string resource
    2: string holder                // 释放时刻的资源闸持有者（空 = 资源空闲）
    3: i64 fence                    // 已发放的最大栅栏令牌（强释不动它，代际继续单调）
    4: i64 expires_at               // 租约到期 Unix 秒（0 = 无活跃租约）
    5: list<ControlPlaneReleaseAction> actions   // 受影响的动作行（挂意图/对账占用）
    6: i64 would_mark_uncertain     // 其中 Unsafe + 在飞意图、正式释放会转入 uncertain 的行数
}

// ControlPlaneReleaseLeaseRequest 强制释放一条资源租约：清资源行持有状态
// + 该资源下全部未裁决执行意图（不复活 done；Unsafe+在飞的行默认转入
// uncertain 等对账/人工裁决——#64 协议，不由租约释放默许重试）。
// 两段式协议：dry_run=true 只读预估（不修改任何行、不落审计）；
// 正式执行必须 confirm=true 且 reason 非空（二次确认 + 操作理由 + 持久化审计）。
struct ControlPlaneReleaseLeaseRequest {
    1: string resource              // 必填，≤255
    2: optional string reason       // 正式执行必填（审计留痕）
    3: bool dry_run                 // true = 只读预估，返回受影响清单
    4: bool confirm                 // 二次确认：正式执行必须显式 true
}

struct ControlPlaneReleaseLeaseResponse {
    1: i64 cleared_actions          // 一并清掉执行意图的 action 行数
    2: i64 marked_uncertain         // 其中转入 uncertain 等待对账的行数
    3: optional ControlPlaneReleasePlan plan    // dry_run=true 时的预估清单
    4: i64 audit_id                 // 持久化审计行 ID（正式执行 > 0）
}

// ControlPlaneCancelTaskRequest 终止任务：立即 failed、取消自动重试；在途执行
// 在下一个检查点停止产生副作用（不承诺强杀在途 LLM/工具调用）。
struct ControlPlaneCancelTaskRequest {
    1: string task_id               // 必填
    2: optional string reason
}

struct ControlPlaneCancelTaskResponse {
    1: string task_id
    2: string status                // failed
    3: bool cancelled               // false = 重复终止（幂等）
}