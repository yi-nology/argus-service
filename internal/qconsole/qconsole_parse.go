package qconsole

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func (h *HTTPSource) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// extractCounters prom 文本面提取：metric 名匹配（任一 label 组合）→ 按展示
// 名聚合计数；HELP/TYPE 行跳过。返回非零项（零值计数不上屏）。
func extractCounters(text string, defs []struct {
	metric string
	label  string
}) []ReasonCount {
	out := make([]ReasonCount, 0, len(defs))
	for _, d := range defs {
		total := parseMetricSum(text, d.metric)
		if total > 0 {
			out = append(out, ReasonCount{Name: d.label, Count: total})
		}
	}
	return out
}

// pluginRows 插件表现：按 plugin 标签展开成「插件 × 计数」行（失败/跳过等
// 口径合并总量——插件维度的健康一眼面；明细钻取走 /metrics 原面）。
func pluginRows(text string, defs []struct {
	metric string
	label  string
}) []ReasonCount {
	agg := map[string]*ReasonCount{}
	order := []string{}
	for _, d := range defs {
		for name, count := range parseMetricByLabel(text, d.metric, "plugin") {
			if count == 0 {
				continue
			}
			r, ok := agg[name]
			if !ok {
				r = &ReasonCount{Name: name}
				agg[name] = r
				order = append(order, name)
			}
			r.Count += count
		}
	}
	out := make([]ReasonCount, 0, len(order))
	for _, name := range order {
		out = append(out, *agg[name])
	}
	return out
}

// parseMetricSum prom 指标总和（跨 label 组合）。
func parseMetricSum(text, metric string) uint64 {
	var total uint64
	for _, v := range parseMetricByLabel(text, metric, "") {
		total += v
	}
	return total
}

// parseMetricByLabel prom 文本解析：返回 series → label 值（labelKey 为空 =
// series 原行计数）。容错口径：只认 `NAME{...} VALUE` 与 `NAME VALUE` 行。
func parseMetricByLabel(text, metric, labelKey string) map[string]uint64 {
	out := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := cutMetric(line)
		if !ok || name != metric {
			continue
		}
		labelVal := ""
		if strings.HasPrefix(rest, "{") {
			end := strings.Index(rest, "}")
			if end < 0 {
				continue
			}
			if labelKey != "" {
				labelVal = labelValue(rest[1:end], labelKey)
			}
			rest = rest[end+1:]
		} else if labelKey != "" {
			continue // 无标签 series 不归属任何插件
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		out[labelVal] += v
	}
	return out
}

// cutMetric 切出指标名与其余部分。
func cutMetric(line string) (name, rest string, ok bool) {
	i := strings.IndexAny(line, "{ ")
	if i < 0 {
		return "", "", false
	}
	return line[:i], line[i:], true
}

// labelValue label 文本里取指定键的值（naive 解析，prom 文本足够）。
func labelValue(labels, key string) string {
	for _, part := range strings.Split(labels, ",") {
		part = strings.TrimSpace(part)
		if kv, ok := strings.CutPrefix(part, key+`="`); ok {
			if v, ok2 := strings.CutSuffix(kv, `"`); ok2 {
				return v
			}
		}
	}
	return ""
}

// tenantRows 租户维度展开（#110）：argus_* 关键指标的 tenant label 聚合成
// per-tenant 运营行（任务终态/LLM token/队列深度/预算超限）。无 tenant 标签
// 的 series（旧文本面/基础设施指标）不归属任何租户行——存量 prometheus 数据
// 不会出现 tenant label，qconsole 对老数据只是少了这块视图，其余不动。
func tenantRows(text string) []TenantRow {
	agg := map[string]*TenantRow{}
	order := []string{}
	get := func(t string) *TenantRow {
		if r, ok := agg[t]; ok {
			return r
		}
		r := &TenantRow{Tenant: t}
		agg[t] = r
		order = append(order, t)
		return r
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := cutMetric(line)
		if !ok {
			continue
		}
		// tenant 标签是所有租户行指标的归属判据；无 tenant 的 series 跳过。
		if !strings.HasPrefix(rest, "{") {
			continue
		}
		end := strings.Index(rest, "}")
		if end < 0 {
			continue
		}
		labels, val := rest[1:end], rest[end+1:]
		t := labelValue(labels, "tenant")
		if t == "" {
			continue
		}
		r := get(t)
		var field *uint64
		switch name {
		case "argus_tasks_total":
			// status 维度折叠成 done/failed 两列（其余 status 忽略——运营行只
			// 关心成败吞吐；中间态由队列深度列承载）。
			switch labelValue(labels, "status") {
			case "done":
				field = &r.TasksDone
			case "failed":
				field = &r.TasksFailed
			default:
				continue
			}
		case "argus_llm_tokens_total":
			field = &r.Tokens
		case "argus_tasks_pending":
			field = &r.Pending
		case "argus_tasks_running":
			field = &r.Running
		case "argus_budget_exceeded_total":
			field = &r.BudgetExceeded
		default:
			continue
		}
		fields := strings.Fields(val)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		*field += v
	}
	out := make([]TenantRow, 0, len(order))
	for _, t := range order {
		out = append(out, *agg[t])
	}
	return out
}
