// service_impl_impact.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"maps"
	"slices"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/oagentkit/rag"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/impactview"
	"github.com/yi-nology/argus/internal/tenant"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// prevImpactCompare 的包级实现（转发壳见原文件）。
func prevImpactCompare(s *Service, ctx context.Context, cur *storage.Task) *impactview.ImpactCompare {
	tasks, err := s.Store.ListTasksByPR(ctx, cur.Platform, cur.Owner, cur.Repo, cur.Number, 6)
	if err != nil {
		return nil
	}
	for i := range tasks {
		pt := &tasks[i]
		if pt.ID == cur.ID || pt.Status != storage.StatusDone {
			continue
		}
		raw := storage.DecodeJSONMap(pt.ArtifactsJSON)[plugin.ArtifactImpactView]
		if raw == "" {
			continue
		}
		var prev impactview.Snapshot
		if json.Unmarshal([]byte(raw), &prev) != nil {
			continue
		}
		var curSnap impactview.Snapshot
		if raw2 := storage.DecodeJSONMap(cur.ArtifactsJSON)[plugin.ArtifactImpactView]; raw2 != "" {
			if json.Unmarshal([]byte(raw2), &curSnap) != nil {
				return nil
			}
		}
		return impactview.CompareSnapshots(&prev, &curSnap)
	}
	return nil
}

// impactShareVerifier 的包级实现（转发壳见原文件）。
// #91：share 免登面补租户绑定 + 访问审计——校验前按任务 ID 反查租户（存储
// 谓词旁路：share 令牌本身就是跨租户路由凭证），命中即记审计行（此前 share
// 访问零审计）。
func impactShareVerifier(s *Service) func(*app.RequestContext) bool {
	secret := s.Cfg.APIToken
	return func(c *app.RequestContext) bool {
		taskID := c.Param("taskId")
		tid := tenant.Default
		if s.Store != nil {
			if t, err := s.Store.GetTask(tenant.WithoutScope(context.Background()), taskID); err == nil && t != nil {
				tid = t.TenantID
			}
		}
		if !impactview.ImpactShareValid(secret, tid, taskID, c.Query("share")) {
			return false
		}
		s.Audit.Log("impact.share_access", "task_id", taskID, "tenant", tid)
		return true
	}
}

// impactSnapshot 的包级实现（转发壳见原文件）。
func impactSnapshot(s *Service, ctx context.Context, taskID string) (*impactview.Snapshot, *storage.Task, bool) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, nil, false
	}
	if !s.taskVisible(ctx, task.Platform, task.Owner, task.Repo) {
		return nil, nil, false
	}
	raw := storage.DecodeJSONMap(task.ArtifactsJSON)[plugin.ArtifactImpactView]
	if raw == "" {
		return nil, task, false
	}
	var snap impactview.Snapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return nil, task, false
	}
	return &snap, task, true
}

// impactView 的包级实现（转发壳见原文件）。
func impactView(s *Service, ctx context.Context, taskID string) (map[string]any, bool) {
	snap, task, ok := s.impactSnapshot(ctx, taskID)
	if !ok {
		return nil, false
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	m["task_id"] = task.ID
	m["task_status"] = task.Status
	// v5.8.1 演化对比数据面（MCP get_impact 消费）
	if cmp := s.prevImpactCompare(ctx, task); cmp != nil {
		m["compare"] = cmp
	}
	return m, true
}

// impactViewHTML 的包级实现（转发壳见原文件）。
func impactViewHTML(s *Service, ctx context.Context, taskID string) (string, bool) {
	snap, task, ok := s.impactSnapshot(ctx, taskID)
	if !ok {
		return "", false
	}
	// v5.8.0 审查意见挂图：findings 从任务 ResultJSON 解码（两态单源同 GetTask）
	fs, _ := taskFindings(task.ResultJSON)
	// v5.8.1 演化对比：同 MR 上一轮快照集合差进页头摘要
	cmp := s.prevImpactCompare(ctx, task)
	// v5.8.2 惯犯文件热力：prints 跨 MR 意见史（改动文件集合聚合）
	hist, _ := s.Store.FindingHistoryByFiles(ctx, task.Platform, task.Owner, task.Repo, snap.Changed)
	page := impactview.RenderHTMLFull(snap, task.ID, impactview.ViewOpts{
		Findings: fs, Compare: cmp, History: hist,
	})
	// v5.6.4 图谱互跳：影响图页头注入整仓地图链接（同源相对路径；带 repo-map
	// share 令牌——share 访客（免登看任务图谱）点过去不落 401；secret 空=
	// 鉴权关闭时裸链即可）。
	if page != "" {
		link := repoMapLink(s.Cfg.APIToken, tenant.FromContext(ctx), task.Platform, task.Owner, task.Repo)
		page = strings.Replace(page, "<header>",
			`<header><a class="meta" href="`+html.EscapeString(link)+`" style="margin-left:auto">🗺 整仓地图</a>`, 1)
	}
	return page, true
}

// searchKnowledge 的包级实现（转发壳见原文件）。
func searchKnowledge(s *Service, ctx context.Context, query string, topK int) (string, error) {
	if s.Knowledge == nil {
		return "", fmt.Errorf("知识库未启用（未配置 ARGUS_KNOWLEDGE_DIR 或 ARGUS_MILVUS_ADDR+ARGUS_EMBEDDING_*）")
	}
	if _, isNoop := s.Knowledge.(*rag.Noop); isNoop {
		return "", fmt.Errorf("知识库未启用（未配置 ARGUS_KNOWLEDGE_DIR 或 ARGUS_MILVUS_ADDR+ARGUS_EMBEDDING_*）")
	}
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query 为空")
	}
	if topK <= 0 {
		topK = knowledgeTopKDefault
	}
	if topK > knowledgeTopKMax {
		topK = knowledgeTopKMax
	}
	chunks, err := s.Knowledge.Retrieve(ctx, query, topK, nil)
	if err != nil {
		return "", fmt.Errorf("知识检索失败: %w", err)
	}
	if len(chunks) == 0 {
		return "知识库无命中（可换关键词重试；规范类问题请直接陈述你的做法征求确认）", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "search_knowledge（query=%s）命中 %d 条，按相关度降序：\n", query, len(chunks))
	for i, c := range chunks {
		content, _ := textutil.TruncRunes(c.Content, knowledgeChunkCap)
		fmt.Fprintf(&b, "\n[%d] score=%.3f", i+1, c.Score)
		if len(c.Metadata) > 0 {
			keys := slices.Sorted(maps.Keys(c.Metadata))
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, k+"="+c.Metadata[k])
			}
			fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
		}
		b.WriteString("\n" + content + "\n")
	}
	return plugin.DataFence("search_knowledge 检索结果（数据）", b.String()), nil
}

// searchSem 的包级实现（转发壳见原文件）。
func searchSem(s *Service, ctx context.Context, platform, owner, repo, number, query string, topK int) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	return s.qa().SearchSem(ctx, platform, owner, repo, number, query, topK)
}

// searchFindingsHistory 的包级实现（转发壳见原文件）。
func searchFindingsHistory(s *Service, ctx context.Context, platform, owner, repo, query string, limit int) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	if limit <= 0 {
		limit = findingsHistoryDefault
	}
	if limit > findingsHistoryMax {
		limit = findingsHistoryMax
	}
	items, err := s.Store.SearchFindingsHistory(ctx, platform, owner, repo, query, limit)
	if err != nil {
		return "", fmt.Errorf("历史意见检索失败: %w", err)
	}
	if len(items) == 0 {
		return "本仓无匹配的历史意见记录（可换关键词，或该问题此前未被报过）", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "search_findings（repo=%s/%s query=%q）命中 %d 条历史意见，按最近出现倒序：\n", owner, repo, query, len(items))
	for i, it := range items {
		comment, _ := textutil.TruncRunes(it.Comment, historyCommentCap)
		fmt.Fprintf(&b, "\n[%d] %s:%d %s [%s ×%d] fp:%s\n%s\n",
			i+1, it.File, it.Line, it.Severity, it.Status, it.TimesSeen, it.FP, comment)
	}
	return plugin.DataFence("search_findings 检索结果（数据）", b.String()), nil
}

// trace 的包级实现（转发壳见原文件）。
func trace(s *Service, ctx context.Context, platform, owner, repo, number, from, to string) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	return s.qa().Trace(ctx, platform, owner, repo, number, from, to)
}

// symContext 的包级实现（转发壳见原文件）。
func symContext(s *Service, ctx context.Context, platform, owner, repo, number, symbol string) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	return s.qa().SymContext(ctx, platform, owner, repo, number, symbol)
}
