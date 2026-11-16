# Langfuse 接入（LLM 可观测台）

argus 的 LLM 调用计量经**既有 OTel OTLP 管线**发往 Langfuse（OTLP/HTTP 是
Langfuse 官方一等公民通路；Go 无官方 SDK，不引第三方）。span 语义：
`gen_ai.*`（模型/token/耗时/finish_reason，Langfuse 自动映射为 generation）+
`langfuse.*`（session/user/trace 归因）。**不发 prompt/completion 文本**——
计量与归因先行，内容面涉 PII 纪律暂缓（后续可 opt-in）。

## 观测形态

- **Session = 任务**：`langfuse.session.id`=task_id——同一审查任务的多次 LLM
  调用（主模型/fallback/阶段覆盖/ReAct 旁路）聚合进一个会话页。
- **逐调用 generation**：模型名、input/output/cached/reasoning tokens、延迟
  （span 时长）、finish_reason、iteration 序号、Partial（失败面估算）标记。
- **过滤维度**：trace tags=任务类型（code_review/requirement_compliance）+
  stage + 平台；user.id=`platform:owner/repo`。

## 部署（内网自托管）

```bash
cd deploy/langfuse
# 必填密钥（fail-fast：缺值 compose 直接报错）
export LANGFUSE_SALT=$(openssl rand -hex 32)        ENCRYPTION_KEY=$(openssl rand -hex 32)        NEXTAUTH_SECRET=$(openssl rand -base64 32)
# bootstrap（可选）：启动自建 org/project/keys（幂等），免 UI 首登
export LANGFUSE_PK=pk-lf-argus LANGFUSE_SK=sk-lf-argus
docker compose up -d
python3 create-buckets.py <host>:19000   # minio 建 langfuse/langfuse-events 两桶（幂等）
```

未 bootstrap 时：web http://<host>:3000 首登建组织+项目，项目设置 → API Keys
取 pk/sk。

## 踩坑实录（2026-11 实弹钉死）

- **env 键名**：salt 的键是 `SALT`（无 LANGFUSE_ 前缀；`ENCRYPTION_KEY` 同为
  无前缀）。`REDIS_URL` 不认——v3 用 `REDIS_HOST/REDIS_PORT/REDIS_AUTH`（无
  密码 redis 会被发 AUTH 报错，两端口径必须一致）。
- **ClickHouse 单机三件**：Langfuse v3 建表用 ReplicatedMergeTree——需内置
  Keeper（`clickhouse-single-node.xml`）+ `macros`（{shard}/{replica}）；
  `CLICKHOUSE_MIGRATION_URL` 用原生协议 `clickhouse://host:9000`（非 8123）。
- **S3 桶不自建**：v3 事件上传（`LANGFUSE_S3_EVENT_UPLOAD_*`）要求桶已存在，
  用 `create-buckets.py`（SigV4 手工签名，minio/mc 镜像 2026-09 起已从
  Docker Hub 下架；minio 社区版镜像亦删——用 `cgr.dev/chainguard/minio`）。
- **OTLP 通路**：只支持 OTLP/**HTTP**（无 gRPC）；Basic auth 走导出器 Headers。

## argus 侧配置（三个 env）

```bash
ARGUS_OTEL_ENDPOINT=http://<lf-host>:3000/api/public/otel   # 基址即可，tracing 推导 v1/traces
ARGUS_OTEL_PROTOCOL=http        # 必须 http——Langfuse 不支持 OTLP/gRPC
ARGUS_OTEL_HEADERS="Authorization: Basic <base64(pk:sk)>"
# 可选：ARGUS_OTEL_SAMPLE_RATE=0.1  # 采样（0=全量；GenAI span 每调用一笔，量大可采）
```

注意：
- `ARGUS_OTEL_ENDPOINT` 同时是既有通用 trace（LLM HTTP/DB span）的出口——
  指向 Langfuse 后两者一并入 Langfuse（Langfuse 聚焦 LLM 相关 span，其余
  span 归入 trace 树不额外计费维度）。
- k8s43/Helm 部署：env 注入同键（`deploy/helm/argus/values.yaml` env 段）；
  Langfuse 栈部署在可达网段（同 k3s 或 10.60 段）。

## 语义契约（internal/langfuseobs）

| span 属性 | 来源 | Langfuse 映射 |
|---|---|---|
| `gen_ai.request.model` | UsageRecord.Model | generation 模型 |
| `gen_ai.usage.{total,input,prompt,output,completion,cached,reasoning}_tokens` | UsageRecord | token/成本（新旧 semconv 双发兼容） |
| `gen_ai.response.finish_reason` | FinishReason | 完成原因 |
| `langfuse.session.id` | task.ID | 会话=任务 |
| `langfuse.user.id` | platform:owner/repo | 租户归因 |
| `langfuse.trace.name` | kind platform:owner/repo#number | trace 名 |
| `langfuse.trace.tags` | kind/stage/platform | 过滤标签 |
| `llm.{iteration,partial,stage}` | UsageRecord/Labels | argus 侧维度 |

埋点选点：`llm.NewUsageHandler` sink（全任务唯一用量出口，runner 装配）——
与 usage_events 账本/成本记账同点，口径天然一致。
