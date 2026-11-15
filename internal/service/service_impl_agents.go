// service_impl_agents.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// listAgents 的包级实现（转发壳见原文件）。
func listAgents(s *Service, ctx context.Context, platform, owner, repo string) *argus.ListAgentsResponse {
	resp := &argus.ListAgentsResponse{}
	for _, reg := range s.Registry.Snapshot() {
		m := reg.Manifest
		dto := &argus.AgentManifestDTO{
			Name:          m.Name,
			Kind:          kindToIDL(m.Kind),
			Skills:        m.Skills,
			Enabled:       reg.Enabled,
			Needs:         m.Needs.Strings(),
			TriggerPaths:  m.Triggers.Paths,
			TriggerLabels: m.Triggers.Labels,
		}
		if m.Cost != "" {
			dto.Cost = strPtr(m.Cost)
		}
		if m.Timeout > 0 {
			ms := m.Timeout.Milliseconds()
			dto.TimeoutMs = &ms
		}
		if m.Version != "" {
			dto.Version = strPtr(m.Version)
		}
		if m.Protocol != "" {
			p := protocolToIDL(m.Protocol)
			dto.Protocol = &p
		}
		resp.Agents = append(resp.Agents, dto)
	}

	if platform != "" && owner != "" && repo != "" {
		resp.RepoConfig = s.repoConfigView(ctx, platform, owner, repo)
	}
	return resp
}

// repoConfigView 的包级实现（转发壳见原文件）。
func repoConfigView(s *Service, ctx context.Context, platform, owner, repo string) *argus.RepoConfigView {
	view := &argus.RepoConfigView{Loaded: false}
	addWarn := func(msg string) {
		view.Warnings = append(view.Warnings, msg)
	}
	platform, _, ok := s.requirePlatform(platform)
	if !ok {
		addWarn("平台未接入: " + platform)
		return view
	}
	rc, err := s.SDK.LoadRepoConfigAuto(ctx, platform, owner, repo, "")
	if err != nil {
		addWarn("读取 .reviewagents.yaml 失败: " + err.Error())
		return view
	}
	view.Loaded = rc.Loaded
	statuses, warnings := dispatch.ObserveRepoConfig(s.Registry, rc)
	dispatch.SortStatuses(statuses)
	view.Use = rc.Use
	view.Disable = rc.Disable
	for _, rule := range rc.Routing {
		view.Routing = append(view.Routing, &argus.RoutingRuleView{
			Paths: rule.Paths, Labels: rule.Labels, Use: rule.Use,
		})
	}
	view.Warnings = append(view.Warnings, warnings...)
	for _, st := range statuses {
		item := &argus.RepoAgentStatus{
			Name:       st.Name,
			RepoEffect: st.Effect,
			Note:       strPtrOrEmpty(st.Note),
		}
		if len(st.RoutedBy) > 0 {
			ids := make([]string, 0, len(st.RoutedBy))
			for _, i := range st.RoutedBy {
				ids = append(ids, fmt.Sprintf("%d", i))
			}
			item.RoutedBy = strPtr(strings.Join(ids, ","))
		}
		view.Plugins = append(view.Plugins, item)
	}
	return view
}

// taskStages 的包级实现（转发壳见原文件）。
func taskStages(s *Service, ctx context.Context, taskID string) (map[string]any, bool) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, false
	}
	if !s.taskVisible(ctx, task.Platform, task.Owner, task.Repo) {
		return nil, false // 域外任务按不存在处理（不泄露存在性）
	}
	stageDone := deriveStageDone(
		storage.DecodeJSONMap(task.StageArtifacts),
		storage.DecodeJSONMap(task.DebugJSON),
		task.ResultJSON, task.Verdict, task.Status)
	stages := make([]map[string]any, 0, len(plugin.StageTimeline))
	for _, st := range plugin.StageTimeline {
		stages = append(stages, map[string]any{"stage": st, "done": stageDone[st]})
	}
	return map[string]any{
		"task_id": task.ID, "status": task.Status, "verdict": task.Verdict,
		"stages": stages,
	}, true
}

// findings 的包级实现（转发壳见原文件）。
func findings(s *Service, ctx context.Context, taskID string) ([]map[string]any, bool) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, false
	}
	if !s.taskVisible(ctx, task.Platform, task.Owner, task.Repo) {
		return nil, false // 域外任务按不存在处理
	}
	findings, ok := taskFindings(task.ResultJSON)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(findings))
	for _, f := range findings {
		out = append(out, findingDTO(f))
	}
	return out, true
}

// searchCode 的包级实现（转发壳见原文件）。
func searchCode(s *Service, ctx context.Context, platform, owner, repo, number, pattern, glob string, ignoreCase bool) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	return s.qa().SearchCode(ctx, platform, owner, repo, number, pattern, glob, ignoreCase)
}

// suggestion 的包级实现（转发壳见原文件）。
func suggestion(s *Service, ctx context.Context, taskID, fpPrefix string) (map[string]any, bool) {
	findings, ok := s.Findings(ctx, taskID)
	if !ok {
		return nil, false
	}
	for _, f := range findings {
		fp, _ := f["fp"].(string)
		if fp != "" && strings.HasPrefix(fp, fpPrefix) {
			return f, true
		}
	}
	return nil, false
}
