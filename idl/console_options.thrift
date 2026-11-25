// console_options.thrift —— 控制台选项面类型文件：GET /console/options 聚合词表 DTO。
// v8.7.0 控制台可用性迭代：前端坐标选择器/任务类型下拉的单源数据面——
// platforms（config.SupportedPlatforms）/ kinds（domain.taskkind registry 经
// kindIDLTable 派生 IDL 数值码）/ statuses（storage.AllTaskStatuses）/
// repos（tenant_repos 登记面，行过滤同 statsOverview 先例）。
// include 规则：域文件必须自行 include 它引用的类型文件（thrift 作用域无传递性）。
namespace go argus

include "task.thrift"

// 下拉选项·仓库条目（tenant_repos 登记面投影，行过滤在 service 层）。
struct RepoOption {
    1: required string platform,
    2: required string owner,
    3: required string repo,
}

// 下拉选项·任务类型条目：value=IDL TaskKind 枚举数值（CreateTask wire 面直接可用），
// name=canonical 词表名（domain/taskkind.go 单源）。
struct KindOption {
    1: required task.TaskKind value,
    2: required string name,
}

struct ConsoleOptionsResponse {
    1: required list<string> platforms,
    2: required list<KindOption> kinds,
    3: required list<string> statuses,
    4: required list<RepoOption> repos,
}
