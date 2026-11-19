// service_impl_webhook.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/oekit/concurrency/ratelimit"
	"golang.org/x/time/rate"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/commentcmd"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"
)

// webhook 的包级实现（转发壳见原文件）。
func webhook(s *Service, ctx context.Context, c *app.RequestContext, platform string) (*argus.WebhookResponse, int) {
	ctx, ev, delivery, resp, status, ok := s.webhookAdmission(ctx, c, platform)
	if !ok {
		return resp, status
	}

	// GitHub App 安装事件：记录安装信息，不建审查任务
	if ev.Type == "installation" || ev.Type == "installation_repositories" {
		return s.handleInstallationEvent(ev, delivery), http.StatusOK
	}

	// Q&A 评论路由：issue_comment 事件中含 @argus ask → 直接 LLM 回答（不建 task）。
	// 异步执行（深挖 P0）：ReAct 可达数分钟，同步跑会吃满平台 webhook 超时——
	// 断连取消 ctx 杀掉 ReAct，平台重试又撞 30s 节流，用户永无回复。经
	// SubmitComment（v3.8.0）：WithoutCancel 保 trace + 超时上界 + 有界并发 +
	// 停机排水；delivery 幂等已下沉 DB（MarkCommentDelivery，多副本一致）。
	if commentcmd.IsCommentEvent(ev) {
		if !s.SubmitComment(ctx, ev, delivery) {
			s.log().Warn("argus.comment.busy", "platform", platform, "event_id", delivery)
			w := whResp(whBusy, "评论处理并发已满，请稍后重试")
			w.EventId = strPtr(delivery)
			return w, http.StatusOK
		}
		w := whResp(whAccepted, "qa comment")
		w.EventId = strPtr(delivery)
		return w, http.StatusOK
	}

	// CI 失败事件（v3.71.0，SDK Pipeline Hook 解析）：建 ci_analysis 任务走
	// 写作面归因（失败 job 日志 → LLM → PR 评论）。pipeline 事件没有完整 CR
	// 语义（可能无 MR），不过 CR 过滤链，走独立的轻量建任务分支。
	if strings.HasPrefix(ev.Type, "pipeline.") {
		if ev.CommitSHA == "" || ev.Repo == nil {
			s.log().Info("argus.webhook.skipped", "platform", platform, "event", ev.Type, "reason", "pipeline 事件缺 commit/repo 信息")
			obs.IncrWebhookSkipped(ctx, "pipeline_incomplete")
			w := whResp(whSkipped, "pipeline 事件缺 commit/repo 信息")
			w.EventId = strPtr(delivery)
			return w, http.StatusOK
		}
		return s.pipelineCreateTask(ctx, ev, delivery, platform)
	}

	// §5.1 规则 1-3：事件白名单 / bot 自审 / draft 与异常事件防御
	skipped := func(reason, why string) (*argus.WebhookResponse, int) {
		// 深挖修复（v3.8.0）：此前 skipped 零日志零指标，整类事件被丢在生产不可观测。
		s.log().Info("argus.webhook.skipped", "platform", platform, "event", ev.Type, "reason", why)
		obs.IncrWebhookSkipped(ctx, reason)
		w := whResp(whSkipped, why)
		w.EventId = strPtr(delivery)
		return w, http.StatusOK
	}
	if resp, status, ok := s.webhookCRFilter(ctx, ev, skipped); !ok {
		return resp, status
	}

	return s.webhookCreateTask(ctx, ev, delivery, platform)
}

// webhookAdmission 的包级实现（转发壳见原文件）。
// 返回首参为租户归属 ctx（#82：入站事件按 tenant_repos 映射归租户——下游
// 建任务/评论/抑制/幂等预查全走该租户谓词；无映射 = default 租户）。
func webhookAdmission(s *Service, ctx context.Context, c *app.RequestContext, platform string) (
	ctxOut context.Context, ev *model.NormalizedEvent, delivery string, resp *argus.WebhookResponse, status int, ok bool) {
	ctxOut = ctx
	platform, pcfg, ok := s.requirePlatform(platform)
	if !ok {
		return ctxOut, nil, "", whResp(whError, "平台未接入: "+platform), http.StatusNotFound, false
	}
	// fail-closed（与 🔒 路由同一纪律，v3.0.2 起）：空 secret 时 SDK 跳过签名校验，
	// 该平台 webhook 等于无鉴权——任何能访问本端口的设备都能伪造事件烧 LLM 预算/
	// 发评论。此前只做启动期告警，请求期照常放行；现直接拒绝直到补配 ARGUS_<P>_SECRET。
	if pcfg.Secret == "" {
		obs.IncrWebhookRejected(ctx, "no_secret")
		s.log().Warn("argus.webhook.rejected_no_secret", "platform", platform,
			"hint", "ARGUS_"+strings.ToUpper(platform)+"_SECRET 未配置，webhook 拒绝服务（fail-closed）")
		return ctxOut, nil, "", whResp(whError, "平台 webhook secret 未配置，拒绝服务（配置 "+"ARGUS_"+strings.ToUpper(platform)+"_SECRET 后恢复）"), http.StatusServiceUnavailable, false
	}

	// Content-Length 预检：body 读取前拒绝超限请求，节省内存分配。
	// Hertz WithMaxRequestBodySize 作为第二道防线（框架层拦截）。
	if s.Cfg.WebhookMaxBody > 0 {
		if cl := string(c.GetHeader("Content-Length")); cl != "" {
			if n, err := strconv.Atoi(cl); err == nil && n > s.Cfg.WebhookMaxBody {
				obs.IncrWebhookRejected(ctx, "body_too_large")
				return ctxOut, nil, "", whResp(whError, fmt.Sprintf("请求体过大: %d > %d bytes", n, s.Cfg.WebhookMaxBody)), http.StatusRequestEntityTooLarge, false
			}
		}
	}

	// 幂等键：在 ParseWebhookEvent 之前从原始请求头取 delivery（§5.2 四轮评审修正）
	delivery = s.SDK.ExtractDeliveryID(func(key string) string {
		return string(c.GetHeader(key))
	})

	// 重建 *http.Request（SDK ParseWebhookEvent 需要 net/http 语义做签名校验+解析）
	rawBody := c.Request.Body()
	if len(rawBody) == 0 {
		return ctxOut, nil, delivery, whResp(whError, "读取 body 失败"), http.StatusBadRequest, false
	}
	httpReq, err := http.NewRequest(string(c.Method()), string(c.URI().Path()), bytes.NewReader(rawBody))
	if err != nil {
		return ctxOut, nil, delivery, whResp(whError, "内部请求构造失败"), http.StatusInternalServerError, false
	}
	c.Request.Header.VisitAll(func(k, v []byte) {
		httpReq.Header.Add(string(k), string(v))
	})

	// #82 租户签名密钥候选集：平台全局 secret + 各租户 profile 的该平台
	// secret——签名先于载荷可信，无法先解析再反查租户，只能候选集轮试
	//（候选数=配了该平台密钥的租户数，常数有界）。全候选失败 = 签名拒绝。
	var evParse *model.NormalizedEvent
	var parseErr error
	for _, secret := range s.webhookSecretCandidates(platform, pcfg.Secret) {
		evParse, parseErr = s.SDK.ParseWebhook(platform, httpReq, secret)
		if parseErr == nil || !errors.Is(parseErr, provider.ErrWebhookValidation) {
			break
		}
		// 签名不匹配：重建 body reader 供下一候选重验（ParseWebhook 消费 body）。
		httpReq, _ = http.NewRequest(string(c.Method()), string(c.URI().Path()), bytes.NewReader(rawBody))
		c.Request.Header.VisitAll(func(k, v []byte) {
			httpReq.Header.Add(string(k), string(v))
		})
	}
	ev, err = evParse, parseErr
	if err != nil {
		if errors.Is(err, provider.ErrWebhookValidation) {
			// §10.2：签名校验失败 → 401 拒绝（不产生 task）
			obs.IncrWebhookRejected(ctx, "signature")
			// 拒绝面上下文（调查向）：delivery 幂等键 + 来源地址——此前只有
			// platform，无法区分「某仓 secret 漂移」与「扫描器打端点」。
			s.log().Warn("argus.webhook.signature_rejected", "platform", platform,
				"delivery", delivery, "remote", c.RemoteAddr().String())
			return ctxOut, nil, delivery, whResp(whError, "签名校验失败"), http.StatusUnauthorized, false
		}
		obs.IncrWebhookRejected(ctx, "parse")
		return ctxOut, nil, delivery, whResp(whError, "事件解析失败: "+err.Error()), http.StatusBadRequest, false
	}

	// 仓库坐标闸（#82，顺序在幂等预查之前——未登记坐标连去重回执都不给）：
	// ① 全局白名单：ARGUS_REPO_ALLOWLIST 非空时，清单外坐标一律 skipped
	//    （此前 webhook 是该门的豁免面：共享平台上任意仓库 push 即建任务烧
	//    全局预算——#82 实锤）；② 租户归属：tenant_repos 映射反查，无映射 =
	//    default 租户；③ per-repo 入站限速（此前入站面零限速）。
	repoKey := ""
	if ev.Repo != nil {
		repoKey = platform + ":" + ev.Repo.Owner + "/" + ev.Repo.Name
		if len(s.Cfg.RepoAllowlist) > 0 && !s.webhookRepoAllowed(repoKey) {
			obs.IncrWebhookRejected(ctx, "repo_not_allowlisted")
			s.log().Warn("argus.webhook.repo_not_allowlisted", "repo", repoKey)
			return ctxOut, nil, delivery, &argus.WebhookResponse{
				Status:  whSkipped,
				Reason:  strPtr("仓库不在服务白名单（ARGUS_REPO_ALLOWLIST）"),
				EventId: strPtr(delivery),
			}, http.StatusOK, false
		}
		ctxOut = tenant.NewContext(ctx, s.webhookTenantOf(ctx, platform, ev.Repo.Owner, ev.Repo.Name))
	}
	if repoKey != "" && !s.webhookRateOK(repoKey) {
		obs.IncrWebhookRejected(ctx, "rate_limited")
		s.log().Warn("argus.webhook.rate_limited", "repo", repoKey,
			"rpm", s.Cfg.WebhookRateRPM)
		return ctxOut, nil, delivery, whResp(whError, fmt.Sprintf(
				"该仓库 webhook 频率超限（每仓库 %d 次/分钟，ARGUS_WEBHOOK_RATE_RPM）", s.Cfg.WebhookRateRPM)),
			http.StatusTooManyRequests, false
	}

	// 幂等命中：平台重发 → 返回既有任务（不重复烧 LLM，§5.2；租户谓词已注入——
	// 同 delivery ID 跨租户投递不再被静默吞，#87）。
	if delivery != "" {
		if exist, err := s.Store.GetTaskByEventID(ctxOut, delivery); err == nil && exist != nil {
			s.log().Info("argus.webhook.duplicate_delivery", "event_id", delivery, "task", exist.ID)
			return ctxOut, nil, delivery, &argus.WebhookResponse{
				Status:  "duplicate",
				Reason:  strPtr("重复投递，命中既有任务"),
				EventId: strPtr(delivery),
				TaskId:  strPtr(exist.ID),
			}, http.StatusOK, false
		}
	}
	return ctxOut, ev, delivery, nil, 0, true
}

// webhookSecretCandidates 平台签名密钥候选集（#82）：全局 secret 打头 +
// TenantProfiles 各租户该平台 secret（去重）。签名验证先于载荷可信，
// 租户归属反查在签名之后——候选集轮试是本问题的正解形态。
func (s *Service) webhookSecretCandidates(platform, global string) []string {
	out := []string{global}
	seen := map[string]bool{global: true}
	for _, prof := range s.Cfg.TenantProfiles {
		if pc, ok := prof.Platforms[platform]; ok && pc.Secret != "" && !seen[pc.Secret] {
			seen[pc.Secret] = true
			out = append(out, pc.Secret)
		}
	}
	return out
}

// webhookRepoAllowed 入站坐标白名单判定（#82：matchRepoPattern 复用——
// 清单条目形态与外部入口同一词表，单源不漂移）。
func (s *Service) webhookRepoAllowed(repoKey string) bool {
	for _, e := range s.Cfg.RepoAllowlist {
		if matchRepoPattern(e, repoKey) {
			return true
		}
	}
	return false
}

// webhookTenantOf 入站坐标 → 租户归属（tenant_repos 映射；无映射 = default）。
func (s *Service) webhookTenantOf(ctx context.Context, platform, owner, repo string) string {
	tid, mapped := s.Store.ResolveTenantRepo(ctx, platform, owner, repo)
	if !mapped {
		s.log().Info("argus.webhook.tenant_implicit_default", "repo", platform+":"+owner+"/"+repo)
	}
	return tid
}

// webhookRateOK per-repo 入站限速（#82）。WebhookRateRPM<=0 = 关闭。
func (s *Service) webhookRateOK(repoKey string) bool {
	if s.Cfg == nil || s.Cfg.WebhookRateRPM <= 0 {
		return true
	}
	rpm := s.Cfg.WebhookRateRPM
	s.webhookRateOnce.Do(func() {
		s.webhookRateLimiter = ratelimit.NewKeyedLimiter(func() ratelimit.Limiter {
			return ratelimit.NewTokenBucket(rate.Limit(float64(rpm)/60.0), max(10, rpm/6))
		}, time.Hour)
	})
	return s.webhookRateLimiter.Allow(tokenHash(repoKey))
}

// webhookCRFilter 的包级实现（转发壳见原文件）。
// ctx 形参（#101）：bot 自审判定的租户感知身份消费点——webhook() 传入租户 ctx。
func webhookCRFilter(s *Service, ctx context.Context, ev *model.NormalizedEvent,
	skipped func(reason, why string) (*argus.WebhookResponse, int)) (*argus.WebhookResponse, int, bool) {
	if _, ok := crEventWhitelist[ev.Type]; !ok {
		r, st := skipped("event_type", "事件类型不在白名单: "+ev.Type)
		return r, st, false
	}
	if ev.Actor != nil && ev.Actor.Username != "" && ev.Actor.Username == s.botUsernameOf(ctx) { // #101
		r, st := skipped("bot_self", "bot 自身事件，跳过")
		return r, st, false
	}
	if ev.CR == nil || ev.Repo == nil {
		r, st := skipped("missing_cr", "事件缺少 CR/Repo 信息")
		return r, st, false
	}
	if ev.CR.Draft && s.Cfg.SkipDraft {
		r, st := skipped("draft", "draft PR 默认不审（ARGUS_SKIP_DRAFT=true）")
		return r, st, false
	}
	return nil, 0, true
}

// webhookCreateTask 的包级实现（转发壳见原文件）。
func webhookCreateTask(s *Service, ctx context.Context, ev *model.NormalizedEvent,
	delivery, platform string) (*argus.WebhookResponse, int) {
	if delivery == "" {
		s.log().Warn("argus.webhook.delivery_header_missing",
			"platform", platform, "event", "幂等键缺失，本次投递不做幂等（event_id=NULL）")
	}
	task := storage.NewTask(domain.TaskKindRequirementCompliance, platform, ev.Repo.Owner, ev.Repo.Name, ev.CR.Number)
	task.HeadSHA = ev.CR.HeadSHA
	task.EventType = ev.Type
	task.Priority = 5 // webhook push：作者刚推代码在等，先于 poller 批量（v3.40.0）
	if delivery != "" {
		task.EventID = &delivery
	}

	dupReason, existingID, err := s.Store.CreateTaskForWebhook(ctx, task)
	if err != nil {
		s.log().Error("argus.webhook.task_create_failed", "error", err.Error())
		return whResp(whError, "任务落库失败"), http.StatusInternalServerError
	}
	if dupReason != "" {
		plugin.ReportTaskDeduped(ctx, dupReason) // 去重分布观测（深挖修复：此前只有日志）
	}
	if dupReason == storage.DupLastHeadSHA {
		return dupResp("同 head SHA 已审查过", "", delivery)
	}
	// event_id 唯一冲突（幂等预查与落库之间的并发窗口，store 回查既有任务）：
	// 此前漏判该分支——照常返回新 task.ID（未入库的幻影 ID）+ 发幽灵 SSE 事件。
	if dupReason == storage.DupEventID {
		return dupResp("重复投递，命中既有任务", existingID, delivery)
	}
	if dupReason == storage.DupInFlight {
		return dupResp("同 head SHA 任务执行中", existingID, delivery)
	}

	s.log().Info("argus.webhook.accepted", "task", task.ID, "platform", platform,
		"pr", task.Owner+"/"+task.Repo+"#"+task.Number, "head", ev.CR.HeadSHA, "event_id", delivery)
	s.audit(ctx, auditTaskCreate, "task_id", task.ID, "platform", platform,
		"owner", task.Owner, "repo", task.Repo, "number", task.Number, "source", "webhook")
	s.publishCreated(task)
	return &argus.WebhookResponse{Status: whAccepted, EventId: strPtr(delivery), TaskId: strPtr(task.ID)}, http.StatusOK
}

// pipelineCreateTask 的包级实现（转发壳见原文件）。
func pipelineCreateTask(s *Service, ctx context.Context, ev *model.NormalizedEvent,
	delivery, platform string) (*argus.WebhookResponse, int) {
	number := ""
	if ev.CR != nil {
		number = ev.CR.Number
	}
	task := storage.NewTask(domain.TaskKindCIAnalysis, platform, ev.Repo.Owner, ev.Repo.Name, number)
	task.HeadSHA = ev.CommitSHA
	task.EventType = ev.Type
	task.Priority = 4 // CI 红了作者正盯着，略高于 poller 批量
	if delivery != "" {
		task.EventID = &delivery
	}
	dupReason, existingID, err := s.Store.CreateTaskForWebhook(ctx, task)
	if err != nil {
		s.log().Error("argus.webhook.pipeline_task_create_failed", "error", err.Error())
		return whResp(whError, "任务落库失败"), http.StatusInternalServerError
	}
	if dupReason == storage.DupEventID || dupReason == storage.DupInFlight {
		// in_flight 必须处理（v3.75.3 修复）：同 commit 的 retry pipeline 是新
		// delivery，event_id 拦不住第二次；漏判会以未落库的 task.ID 回 accepted
		// 并向 SSE 广播幽灵任务（对照 webhookCreateTask 的同款处理）。
		reason := "重复投递，命中既有任务"
		if dupReason == storage.DupInFlight {
			reason = "同 commit 同类任务审查中，幂等跳过"
		}
		return dupResp(reason, existingID, delivery)
	}
	s.log().Info("argus.webhook.accepted", "task", task.ID, "platform", platform,
		"kind", task.Kind, "head", task.HeadSHA, "event_id", delivery)
	s.audit(ctx, auditTaskCreate, "task_id", task.ID, "platform", platform,
		"owner", task.Owner, "repo", task.Repo, "number", task.Number, "source", "webhook")
	s.publishCreated(task)
	return &argus.WebhookResponse{Status: whAccepted, EventId: strPtr(delivery), TaskId: strPtr(task.ID)}, http.StatusOK
}
