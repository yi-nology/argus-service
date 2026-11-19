package qconsole

// tenantRows 租户维度展开（#110）：tenant label 聚合成 per-tenant 行；无 tenant
// 的 series（旧指标面/基础设施指标）不归属任何行；status 折叠 done/failed，
// running 等中间态由队列深度列承载。

import "testing"

func TestTenantRowsExpandsByTenantLabel(t *testing.T) {
	text := `# HELP argus_tasks_total x
argus_tasks_total{kind="code_review",platform="gitea",status="done",tenant="acme"} 7
argus_tasks_total{kind="code_review",platform="gitea",status="failed",tenant="acme"} 2
argus_tasks_total{kind="code_review",platform="gitea",status="running",tenant="acme"} 99
argus_llm_tokens_total{model="m",stage="R3",tenant="acme"} 1234
argus_llm_tokens_total{model="m",stage="R3",tenant="beta"} 56
argus_tasks_pending{tenant="beta"} 3
argus_tasks_running{tenant="beta"} 1
argus_budget_exceeded_total{ledger="real",tenant="acme"} 1
argus_sse_connections 42
argus_migration_lease_lost_total 5`
	rows := tenantRows(text)
	if len(rows) != 2 {
		t.Fatalf("租户行数 = %d, want 2（acme/beta）: %+v", len(rows), rows)
	}
	byTenant := map[string]TenantRow{}
	for _, r := range rows {
		byTenant[r.Tenant] = r
	}
	acme := byTenant["acme"]
	if acme.TasksDone != 7 || acme.TasksFailed != 2 || acme.Tokens != 1234 || acme.BudgetExceeded != 1 {
		t.Errorf("acme 行失真: %+v", acme)
	}
	if acme.Pending != 0 || acme.Running != 0 {
		t.Errorf("acme 不应有队列深度（文本未给）: %+v", acme)
	}
	beta := byTenant["beta"]
	if beta.Tokens != 56 || beta.Pending != 3 || beta.Running != 1 {
		t.Errorf("beta 行失真: %+v", beta)
	}
	// running 状态的 tasks_total 不进 done/failed 列。
	if acme.TasksDone == 99 || acme.TasksFailed == 99 {
		t.Errorf("running 状态被误折叠: %+v", acme)
	}
}
