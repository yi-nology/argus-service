// repo_info.thrift —— 仓库信息面类型文件：分支清单 DTO（v8.8.0 控制台分支下拉）。
namespace go argus

// 分支选项（平台 BranchManager 投影，名称升序去重）。
struct RepoBranchesRequest {
    1: required string platform (api.query="platform"),
    2: required string owner (api.query="owner"),
    3: required string repo (api.query="repo"),
}

struct RepoBranchesResponse {
    // 默认分支（GetRepo 面，失败时为空串不阻断清单）。
    1: required string defaultBranch,
    // 分支名清单（升序）。
    2: required list<string> branches,
}

// PR/MR 列表（平台 ChangeRequest 面；控制台 PR 号下拉，v8.8.1）。
struct RepoCRsRequest {
    1: required string platform (api.query="platform"),
    2: required string owner (api.query="owner"),
    3: required string repo (api.query="repo"),
    // 状态过滤：opened|closed|merged；空 = opened（控制台默认只看在开）。
    4: optional string state (api.query="state"),
    // 条数上限（默认 30，最大 100）。
    5: optional i32 limit (api.query="limit"),
}

struct RepoCR {
    1: required string number,
    2: required string title,
    3: optional string state,
    4: optional bool draft,
    5: optional string sourceBranch,
    6: optional string targetBranch,
    7: optional string author,
    8: optional string headSha,
    9: optional string webUrl,
}

struct RepoCRsResponse {
    1: required list<RepoCR> items,
}
