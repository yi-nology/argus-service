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
