// auth.thrift —— 多用户最小集（T5）：登录/邀请/会话/权限投影。
// 角色→scope 映射见 storage.RoleToScope；会话 token 经 Bearer 复用既有中间件。
namespace go argus

// AuthLoginRequest 用户名密码登录。
struct AuthLoginRequest {
    1: required string username
    2: required string password
}

struct AuthLoginResponse {
    1: string token          // 会话 token（Bearer 直接使用）
    2: string username
    3: string role
    4: string scope          // read | ci | admin（角色投影）
    5: i64 expires_at        // Unix 秒
}

// AuthLogoutRequest 登出（撤销当前会话）。
struct AuthLogoutRequest {
    // token 从 Authorization 头取；body 可空
}

struct AuthLogoutResponse {
    1: bool revoked
}

// AuthMeResponse 当前身份 + my_permissions 投影（MergePilot 同名能力）。
struct AuthPermissions {
    1: bool read_repository
    2: bool request_review
    3: bool decide_review        // 审批票 / 合并 / 推送确认
    4: bool apply_fix
    5: bool merge_pr
    6: bool read_audit
    7: bool manage_membership   // 邀请
    8: bool manage_instance     // 控制面
}

struct AuthMeResponse {
    1: string username
    2: string role
    3: string scope
    4: AuthPermissions permissions
    5: optional string user_id
}

// AuthInviteRequest 创建邀请（admin；永不授予 admin 角色）。
struct AuthInviteRequest {
    1: required string role     // viewer | operator | maintainer | auditor
    2: optional string username // 预定用户名（可选）
    3: optional i32 ttl_hours   // 缺省 72
}

struct AuthInviteResponse {
    1: string invite_id
    2: string token             // 明文邀请 token（仅此一次）
    3: string role
    4: i64 expires_at
}

// AuthAcceptInviteRequest 接受邀请并设置密码（公开端点，无需登录）。
struct AuthAcceptInviteRequest {
    1: required string token
    2: required string username
    3: required string password  // ≥8 位
}

struct AuthAcceptInviteResponse {
    1: string username
    2: string role
    3: bool created
}
