// members.thrift —— 实例成员面类型文件：用户清单 DTO（v8.8.0 控制台成员视图；
// 凭证字段不出存储层投影）。
namespace go argus

struct MemberInfo {
    1: required string username,
    2: required string role,
    // 创建时间（unix 秒）。
    3: optional i64 createdAt,
}

struct MembersResponse {
    1: required list<MemberInfo> members,
}
