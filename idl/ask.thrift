// ask.thrift —— 审查问答 HTTP 面（与 MCP ask_argus 同源 ReviewQA）。
namespace go argus

// AskReviewRequest 查询某 PR 最近审查结论 / 追问。
struct AskReviewRequest {
    1: required string platform
    2: required string owner
    3: required string repo
    4: required string number
    5: optional string question   // 空 = 取最新结论；非空 = 基于报告回答
}

// AskLatestRequest GET /ask/review 的 query 绑定（hz 单参数约束）。
struct AskLatestRequest {
    1: required string platform
    2: required string owner
    3: required string repo
    4: required string number
}

struct AskReviewResponse {
    1: bool ok
    2: optional string answer              // 追问回答（markdown）
    3: optional map<string, string> review // 最新结论摘要（verdict/findings/…）
    4: string message
}
