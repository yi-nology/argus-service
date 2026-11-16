// Package arch 可执行架构守卫（v3.62.0）：把「高内聚低耦合」的分层纪律从口头
// 约定变成 go test 门禁。规则 = 当前实现的分层事实快照（2026-09-30 建表，
// 2026-11-11 架构卫生批 B 刷新：销 3 个幽灵条目 + 4 个死条目，补 4 个零约束包），全部当前
// 满足；测试的价值在于**任何依赖方向变更都必须先改本文件**——即架构变更在
// diff 里显式可见、可评审、可回滚。
//
// 五类检查：
//  1. TestLayering        分层白名单：受约束包的 internal 依赖必须是封闭允许集
//     （新增依赖 → 测试失败 → 显式扩集 → CHANGELOG 记档）；
//  2. TestBootstrapNotImported 组合根单向性：bootstrap 只许 import、不许被 import
//     （唯一合法上游 = 仓根 main，#39 起根入口纳入本门禁采集面）；
//  3. TestPackageDocs     包文档完整性：每个 internal 包必须有包级 doc comment
//     （包的职责与层级写在第一眼能看到的地方）；
//  4. TestRootMainGated   仓根入口纳管（#39）：根 main 在采集面内 + 白名单
//     正例/负例断言（kernel archcheck 的 packageDirs 跳过仓库根，本测试补位）；
//  5. TestNoPrintfLogCallback printf 风格日志回调禁令（#42，argus-no-printf-log-callback
//     severity=error）：禁 logger 回调签名 + 禁 stdlib log 留痕，统一 logx.Logger。
//
// 规则与 docs/architecture-deep-dive.md / design.md 的分工：design 是设计契约
// （§ 引用），deep-dive 是历史演进分析，本测试是**当前分层事实的机器可读快照**
// ——三者不一致时，以能跑的为准。
package arch

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// deps 包 → 直接 import 集合（TestMain 一次采集）。
var deps map[string]map[string]bool

func TestMain(m *testing.M) {
	// #39：采集根含仓根 main 包——kernel archcheck 的 packageDirs 跳过仓库根
	//（根 main.go 的 import 不受 dependency_direction 约束），本门禁补位把根
	// 入口纳入依赖方向约束（TestRootMainGated 守护采集面本身不被收窄）。
	out, err := exec.Command("go", "list", "-f",
		`{{.ImportPath}}|{{join .Imports "|"}}`,
		"github.com/yi-nology/argus", "github.com/yi-nology/argus/internal/...").Output()
	if err != nil {
		// 编译失败等场景：让 go test 自身的构建错误先行暴露，这里直接失败
		panic("go list 失败: " + err.Error() + ": " + string(out))
	}
	deps = map[string]map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		imports := map[string]bool{}
		if parts[1] != "" {
			for _, imp := range strings.Split(parts[1], "|") {
				imports[imp] = true
			}
		}
		deps[parts[0]] = imports
	}
	os.Exit(m.Run())
}

// layering 分层白名单。key = 受约束包（精确路径）；value = 允许的 argus/internal/*
// 依赖集合（封闭）。不在表内的包不受 import 方向约束（例外：bootstrap 组合根
// ——装配一切、单向性由 TestBootstrapNotImported 独立守卫，分层允许集对它无
// 意义；#39 起仓根 main 也在表内显式归类）。47 个 internal 包全部在表内。
// 规则变更 = 架构变更：必须同步 docs/architecture-deep-dive.md 并在 CHANGELOG 记档。
var layering = map[string][]string{
	// ---- 仓根入口（#39）：进程生命周期外壳，装配根 bootstrap 的唯一合法上游 ----
	// kernel archcheck 的 packageDirs 跳过仓库根，根 main.go 的 import 不受
	// dependency_direction 约束；本仓不走「移 main 到 cmd/argus」（会动
	// Dockerfile/Makefile 构建入口与 hz 根布局生成物 router*.go），改由本门禁
	// 把 go list 采集根扩到仓根——根入口的依赖方向变更同样必须显式扩集。
	// 允许集 = 2026-11-16 现状 import 快照：bootstrap（组合根装配）+
	// mcpserver（入口适配接线）+ service（biz 接线/SetService）+ config/obs/storage
	// （启动期直需的基础件）。
	"github.com/yi-nology/argus": {
		"github.com/yi-nology/argus/internal/bootstrap",
		"github.com/yi-nology/argus/internal/channel/mcpserver",
		"github.com/yi-nology/argus/internal/config",
		"github.com/yi-nology/argus/internal/obs",
		"github.com/yi-nology/argus/internal/service",
		"github.com/yi-nology/argus/internal/storage",
	},

	// ---- L0 纯底座：零 internal 依赖 ----
	"github.com/yi-nology/argus/internal/codesearch": {},
	// v6.6.0：semsearch chunk 参与面消费 langcap.SemChunkExts（语言能力矩阵单源）。
	"github.com/yi-nology/argus/internal/semsearch": {"github.com/yi-nology/argus/internal/langcap"},
	"github.com/yi-nology/argus/internal/learning":  {},
	"github.com/yi-nology/argus/internal/progress":  {},
	"github.com/yi-nology/argus/internal/gitdeepen": {},
	// v6.6.0：语言能力矩阵单一事实源（纯数据+口径派生，零 internal 依赖）。
	"github.com/yi-nology/argus/internal/langcap": {},
	// v6.10.0：报告契约模板注册表（词表单源 standard|analyst|minimal + analyst
	// 综合段提示词；L0 零依赖叶子，plugin/config/workflow/adapter_builtin/runner 消费）。
	"github.com/yi-nology/argus/internal/reporttmpl": {},
	// P2-01（#24）：故障注入抽象 + 混沌矩阵（五族故障词表/规范矩阵/报告）。
	// L0 零 internal 依赖——注入契约被 test/chaos 实弹回归与部署侧手动 job 消费，
	// 在线链路不 import（评测/混沌旁路形态，同 obs evalmetrics）。
	"github.com/yi-nology/argus/internal/chaos": {},
	// P2-02（#25）：质量评测体系（指标计算/基准集装载/跑批骨架/报告）。
	// L0 零 internal 依赖——真实模型调用只经 Reviewer 接口装配，在线链路不 import。
	"github.com/yi-nology/argus/internal/evalq": {},
	// Phase 4 质量运营：批次环比/有效发现率/修复率计算单源——纯函数零依赖
	//（数据经参数注入，取数面在 storage 与 test 入口）。
	"github.com/yi-nology/argus/internal/qualityops": {},
	// P3-02（#27）：审查质量控制台（只读聚合 + 静态页/CLI 输出形态）。
	// L0 零 internal 依赖——自带轻量解析直拉既有 API 面（/stats /usage /metrics），
	// 不复用 service DTO（反向依赖不可能发生）；#27 趋势面起 service→qconsole
	// 单向消费 LoadEvalTrend（只读报告投影），qconsole 本体仍零依赖。
	"github.com/yi-nology/argus/internal/qconsole": {},
	// v5.10.0：go/types 精确调用边抽取器（semsearch.EdgeAugmenter 档二实现，
	// bootstrap 经 ARGUS_GRAPH_PRECISE opt-in 注入）——唯一 internal 依赖是
	// semsearch 取 NodeKey 键契约单源；bootstrap→goedges 为组合根单向装配
	// （bootstrap 不受本表约束），不另立白名单条目。
	"github.com/yi-nology/argus/internal/goedges": {"github.com/yi-nology/argus/internal/semsearch"},
	// v3.183.0：影响面管线（外部依赖仅 textx/procx，包装接线在 runner）
	// v5.4.7 funcdiff 浅克隆先 deepen（补录：d0db25c 引入消费未同步白名单，门禁已在 v6.3.0 补录转绿）
	// v5.8.0 findings 挂图：渲染层消费 domain.Finding 契约（findingswire 同向）
	// v5.8.2 惯犯热力：ViewOpts.History 用 storage.FindingFileHistory 聚合结果类型
	// v6.0.0 L2 调用方升级：消歧符号图优先解析调用方（semsearch.NodeKey 键契约
	// + SymbolContext 邻接；纯底座消费，方向与 goedges 同款）
	"github.com/yi-nology/argus/internal/impactview": {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/gitdeepen", "github.com/yi-nology/argus/internal/langcap", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/storage"},
	// v3.187.0：服务侧按仓配置（类型+Parse 校验纯函数，消费方 storage/runner/service）
	"github.com/yi-nology/argus/internal/reposettings": {},

	// ---- 域类型层：只依赖更纯的域类型 ----
	"github.com/yi-nology/argus/internal/domain": {"github.com/yi-nology/argus/internal/learning"},

	// ---- 引擎面（v3.173.0 自 plugin 拆出）：契约/观测经别名桥引用 plugin，方向单一 ----
	// v6.2.x 架构卫生批 B：销幽灵边 agentrun/blackboard/llm（三包早已不存在，
	// 职责由 dispatch 自身 + 外部 oagentkit/agent/blackboard、oagentkit/llm 承接）
	// 与死边 storage（未 import）——封闭允许集语义恢复「只查实到边」。
	"github.com/yi-nology/argus/internal/dispatch": {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/learning", "github.com/yi-nology/argus/internal/plugin"},

	// ---- L4 核心域与横切底座 ----
	// v6.2.x 架构卫生批 B：销死边 learning（plugin 实际仅消费 domain）。
	// v6.10.0：report_template 白名单 sanitize 消费 reporttmpl 词表单源。
	"github.com/yi-nology/argus/internal/plugin": {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/reporttmpl"},
	// v6.10.0：ARGUS_REPORT_TEMPLATE 服务级默认解析消费 reporttmpl 词表单源。
	"github.com/yi-nology/argus/internal/config": {"github.com/yi-nology/argus/internal/learning", "github.com/yi-nology/argus/internal/reporttmpl"},
	// obs 的 plugin 依赖是钩子接线（HookLLMTokens 等 → plugin.Hook*，装配期注入实现）；
	// storage 依赖同模式（HookMigrationLeaseLost ← 迁移租约 fencing 触发，P2-05 #16）
	"github.com/yi-nology/argus/internal/obs":    {"github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/storage"},
	"github.com/yi-nology/argus/internal/notify": {"github.com/yi-nology/argus/internal/notify/octopus", "github.com/yi-nology/argus/internal/obs"},

	// ---- 自有协议客户端子包：零 internal 依赖（v3.148.0 octopus 回归本仓）----
	"github.com/yi-nology/argus/internal/notify/octopus": {},

	// ---- 能力层：可依赖核心域与基座，禁编排/入口 ----
	"github.com/yi-nology/argus/internal/reportview": {"github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/reporttmpl"},
	"github.com/yi-nology/argus/internal/toolpool":   {"github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/sastscan":   {"github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/sink":       {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/plugin"},
	// v6.9.0 P1-05（#10）：outbox 事件载荷复用 progress.Event（领域事件类型单源）——
	// storage 序列化/反序列化 outbox 行需要它；progress 零依赖，方向安全。
	"github.com/yi-nology/argus/internal/storage": {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/learning", "github.com/yi-nology/argus/internal/progress"},
	"github.com/yi-nology/argus/internal/worker":  {"github.com/yi-nology/argus/internal/storage"},
	"github.com/yi-nology/argus/internal/health":  {"github.com/yi-nology/argus/internal/learning"},
	// v6.2.x 架构卫生批 B：销死边 obs（argusctx 实际仅消费 config/plugin）。
	"github.com/yi-nology/argus/internal/argusctx": {"github.com/yi-nology/argus/internal/config", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/qa":       {"github.com/yi-nology/argus/internal/codesearch", "github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/storage"},
	"github.com/yi-nology/argus/internal/dream":    {"github.com/yi-nology/argus/internal/learning", "github.com/yi-nology/argus/internal/memorykernel", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/storage"},
	"github.com/yi-nology/argus/internal/poster":   {"github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/reportview", "github.com/yi-nology/argus/internal/storage"},
	// v6.2.x 架构卫生批 B：销死边 plugin（poller 实际未 import plugin）。
	"github.com/yi-nology/argus/internal/poller":   {"github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/obs", "github.com/yi-nology/argus/internal/progress", "github.com/yi-nology/argus/internal/storage"},
	"github.com/yi-nology/argus/internal/ocrdeleg": {"github.com/yi-nology/argus/internal/gitdeepen", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/ocrshim":  {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/gitdeepen", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/plugin/adapter_cli"},

	// ---- v6.2.x 架构卫生批 B 补边（原 layering 盲区 4 包，此前纯靠自觉）----
	// 允许集 = 现状 import 快照（2026-11-11 go list 实测）：四包均有稳定消费方
	// （service→commentcmd、runner→fixverifier/writing、dream/service→memorykernel），
	// 当前依赖面恰好干净——立此约束后新增 internal 依赖须显式扩集并记档。
	"github.com/yi-nology/argus/internal/commentcmd":   {},
	"github.com/yi-nology/argus/internal/fixverifier":  {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/memorykernel": {},
	"github.com/yi-nology/argus/internal/writing":      {},

	// ---- 编排层：依赖能力层，禁跨编排互依 ----
	"github.com/yi-nology/argus/internal/workflow/requirement": {
		"github.com/yi-nology/argus/internal/codesearch", "github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/domain",
		"github.com/yi-nology/argus/internal/dispatch",
		"github.com/yi-nology/argus/internal/obs", "github.com/yi-nology/argus/internal/plugin",
		// v5.11.0：图面进审查管线——Workflow.Graph 字段与 get_trace/
		// get_symbol_context 工具渲染消费 semsearch（SymRef/Index.Precise）。
		// v6.6.0：get_poly_symbol 描述口径消费 langcap（语言能力矩阵单源）。
		// v6.10.0：分析师综合段消费 reporttmpl 提示词单源（报告契约模板）。
		"github.com/yi-nology/argus/internal/langcap", "github.com/yi-nology/argus/internal/reporttmpl", "github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/toolpool",
	},
	"github.com/yi-nology/argus/internal/runner": {
		// v6.9.0 补 obs 边：预算超限/事件丢弃计数直呼（P2-05 #16 / P1-06 #11）。
		"github.com/yi-nology/argus/internal/obs",
		"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/config", "github.com/yi-nology/argus/internal/argusctx",
		"github.com/yi-nology/argus/internal/dispatch",
		"github.com/yi-nology/argus/internal/fixverifier", "github.com/yi-nology/argus/internal/impactview", "github.com/yi-nology/argus/internal/learning",
		"github.com/yi-nology/argus/internal/memorykernel",
		"github.com/yi-nology/argus/internal/notify", "github.com/yi-nology/argus/internal/ocrdeleg", "github.com/yi-nology/argus/internal/plugin",
		"github.com/yi-nology/argus/internal/poster", "github.com/yi-nology/argus/internal/progress", "github.com/yi-nology/argus/internal/reposettings", "github.com/yi-nology/argus/internal/reporttmpl", "github.com/yi-nology/argus/internal/reportview",
		// v5.11.0：injectCodeGraph 同工作副本再建消歧符号图（semsearch 纯图模式）。
		"github.com/yi-nology/argus/internal/sastscan", "github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/sink", "github.com/yi-nology/argus/internal/storage",
		"github.com/yi-nology/argus/internal/toolpool", "github.com/yi-nology/argus/internal/workflow/requirement", "github.com/yi-nology/argus/internal/writing",
	},
	"github.com/yi-nology/argus/internal/service": {
		// v5.4.4：评论指令词法层剥出（目录/解析/词表纯函数单源，执行体仍在本包）。
		"github.com/yi-nology/argus/internal/commentcmd",
		"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/config", "github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/health",
		"github.com/yi-nology/argus/internal/dispatch",
		"github.com/yi-nology/argus/internal/impactview",
		"github.com/yi-nology/argus/internal/memorykernel", "github.com/yi-nology/argus/internal/obs",
		"github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/progress", "github.com/yi-nology/argus/internal/qa",
		// #27 质量趋势面：/console 读评测基线报告（qconsole.LoadEvalTrend 只读
		// 投影单源）——qconsole 仍零 internal 依赖，方向 service→qconsole 单向。
		"github.com/yi-nology/argus/internal/qconsole",
		// v6.11.0：--template 任务级覆盖的词表校验（reporttmpl 消费，commentcmd 保持零依赖）。
		"github.com/yi-nology/argus/internal/reporttmpl",
		"github.com/yi-nology/argus/internal/reposettings", "github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/storage",
		// v3.123.0：diff 预审 prompt 单源下沉 writing.Service，service.ReviewDiff
		// 委派（与 runner→writing 同向；同 qa 消费先例）。
		"github.com/yi-nology/argus/internal/writing",
	},

	// ---- 入口适配层：channel 靠消费侧窄接口（Core/Copilot/ReviewQA）实现
	// 零 internal 依赖——service 由 main 装配注入，mcpserver 永不见 runner/service ----
	"github.com/yi-nology/argus/internal/channel/mcpserver": {},

	// ---- 适配器子包：单向依赖核心域（plugin 不得反向 import 适配器）----
	"github.com/yi-nology/argus/internal/plugin/adapter_acpx":    {"github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/plugin/adapter_builtin": {"github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/reporttmpl"},
	"github.com/yi-nology/argus/internal/plugin/adapter_cli":     {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/plugin/adapter_remote":  {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/plugin"},
}

// unclassifiedSentinel 未归类包的违规哨兵（layeringViolations 返回值约定）。
const unclassifiedSentinel = "<未在 layering 白名单归类>"

// layeringViolations 计算 pkg 对 imports 的白名单外 internal 依赖（排序保证
// 输出稳定，供门禁与 TestRootMainGated 的正例/负例断言复用）。bootstrap 例外：
// 组合根由 TestBootstrapNotImported 单独守卫（装配根允许触达一切，分层允许集
// 对它无意义）；未归类包返回哨兵而非空——「不在白名单」本身就是违规（v6.3.0
// 评估 P2 修复：此前新包靠「不在白名单」直接跳过约束——盲区）。
func layeringViolations(pkg string, imports map[string]bool) []string {
	allowed, constrained := layering[pkg]
	if !constrained {
		if pkg == "github.com/yi-nology/argus/internal/bootstrap" {
			return nil
		}
		return []string{unclassifiedSentinel}
	}
	var bad []string
	for imp := range imports {
		if !strings.HasPrefix(imp, "github.com/yi-nology/argus/internal/") {
			continue
		}
		if !slices.Contains(allowed, imp) {
			bad = append(bad, imp)
		}
	}
	sort.Strings(bad)
	return bad
}

// TestLayering 分层白名单守卫。
func TestLayering(t *testing.T) {
	for pkg, imports := range deps {
		for _, v := range layeringViolations(pkg, imports) {
			if v == unclassifiedSentinel {
				t.Errorf("%s 未在 layering 白名单归类：新包必须显式声明分层位置"+
					"（允许集可为空 {}，如自有协议子包）", pkg)
				continue
			}
			t.Errorf("%s → %s：白名单外依赖。\n"+
				"这是架构变更：请确认方向正确后更新 test/arch 的 layering 白名单，"+
				"并同步 docs/architecture-deep-dive.md 与 CHANGELOG 记档。", pkg, v)
		}
	}
}

// TestBootstrapNotImported 组合根单向性：bootstrap 装配一切但不被任何 internal
// 包 import。仓根 main（#39 起在采集面内）是唯一合法上游——组合根被其他包
// 反向依赖意味着装配职责泄漏，装配图不再单点可见。
func TestBootstrapNotImported(t *testing.T) {
	for pkg, imports := range deps {
		if pkg == "github.com/yi-nology/argus/internal/bootstrap" {
			continue
		}
		if pkg == "github.com/yi-nology/argus" {
			continue // 仓根 main：进程生命周期外壳，组合根唯一合法上游
		}
		if imports["github.com/yi-nology/argus/internal/bootstrap"] {
			t.Errorf("%s → argus/internal/bootstrap：组合根被反向依赖（装配职责泄漏）", pkg)
		}
	}
}

// TestRootMainGated 仓根入口纳管断言（#39）：kernel archcheck 的 packageDirs
// 跳过仓库根（根 main.go 的 import 不受 dependency_direction 约束），本门禁把
// 采集根扩到仓根补位。三条断言：
//  1. 根 main 在采集面内（go list 模式被收窄即红——门禁覆盖面本身受守卫）；
//  2. 正例：根 main 现有 internal 依赖全部过闸（layering 快照与现状漂移即红）；
//  3. 负例：给根 main 合成一个白名单外依赖（internal/runner），门禁必须报红
//     ——证明约束对根入口真实生效，不是「在表内但从不判」的摆设。
func TestRootMainGated(t *testing.T) {
	root := "github.com/yi-nology/argus"
	imports, ok := deps[root]
	if !ok {
		t.Fatalf("仓根 main 包不在门禁采集面内：TestMain 的 go list 模式必须含 %q"+
			"（#39：根入口的依赖方向变更须过本门禁）", root)
	}
	if v := layeringViolations(root, imports); len(v) > 0 {
		t.Fatalf("根 main 现有依赖被判白名单外（layering 快照漂移）: %v", v)
	}
	bad := map[string]bool{"github.com/yi-nology/argus/internal/runner": true}
	if v := layeringViolations(root, bad); len(v) == 0 {
		t.Fatalf("负例未报红：根 main → internal/runner 应判白名单外依赖" +
			"（依赖方向门禁对根入口未生效）")
	}
}

// docAllowlist 包文档检查的暂缓名单（原因必填）。当前为空——v3.62.0 遗留的
// domain「迁移在途」条目已死（domain 现有包文档），v6.2.x 架构卫生批 B 销账。
var docAllowlist = map[string]string{}

// TestPackageDocs 包文档完整性：包级 doc comment 是架构可读性的第一道门——
// 职责、层级、边界写在 go doc 第一屏。
func TestPackageDocs(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}}|{{.Doc}}`,
		"github.com/yi-nology/argus/internal/...").Output()
	if err != nil {
		t.Fatalf("go list 失败: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		pkg, doc := parts[0], parts[1]
		if doc == "" {
			if reason, ok := docAllowlist[pkg]; ok {
				t.Logf("包 %s 缺 doc（暂缓：%s）", pkg, reason)
				continue
			}
			t.Errorf("包 %s 缺包级 doc comment：请在包内任一文件 package 子句上方补「// Package <name> 职责一句话（层级/边界要点）」", pkg)
		}
	}
}

// ---- argus-no-printf-log-callback（#42，severity=error）----
//
// printf 风格日志回调一旦焊进公开 API 签名就是不可逆的 API 债；stdlib log 包
// 则绕开 logx 的级别/脱敏/trace 管线（FilterKey 凭证兜底只挂在 logx 装配口）。
// 本规则 severity=error（测试失败即非零退出）——warn 只进报告不阻断，等于没有。
// 存量已于本批清零（internal/ocrshim + cmd/ocrshim 共 6 处 stdlib log.Printf）。

// printfCallbackRe 「logger 回调」签名形态：名为 logf/logFunc/logFn/logger 的
// 参数，其类型是 func(...) 且首参为 string（首参允许带参数名——那才是 printf
// 回调的常见写法）。判据与 kernel archcheck 的 harness-no-printf-log-callback 同源。
var printfCallbackRe = regexp.MustCompile(`\b(logf|logFunc|logFn|logger)\s+func\(\s*(?:[A-Za-z_]\w*\s+)?string\b`)

// stdlibLogCallRe stdlib log 包留痕调用（Debug/Info/Warn/Error/Fatal/Print 全家族）。
// 判定以「本文件 import 了 stdlib log」为前提（见 TestNoPrintfLogCallback 的
// 两段式）：裸 `\blog\.` 分不清包限定与 receiver 字段——k.log.Warn(...）这类
// logx.Logger 结构体字段调用（memorykernel/argusctx 等 43 处）是合法形态。
var stdlibLogCallRe = regexp.MustCompile(`\blog\.(?:Debug|Info|Warn|Error|Fatal|Print)(?:f|ln)?\(`)

// stdlibLogImportRe stdlib log 包 import（含别名/空白导入；gorm.io/gorm/logger
// 等同名子包路径不命中）。
var stdlibLogImportRe = regexp.MustCompile(`^\s*(?:[_A-Za-z]\w*\s+)?"log"$`)

// TestNoPrintfLogCallback printf 风格日志禁令（#42，severity=error）：全仓 Go
// 源（生产 + 测试）扫两类形态——logger 回调签名（独立判定）；stdlib log 留痕
// （两段式：先判 import，命中才报调用行——receiver 风格 .log.X(...) 的 logx
// 字段调用不误伤）。留痕统一 logx.Logger：进程服务面由 main 装配默认后端
// （logx.SetDefault），SDK/旁路进程包级 logger 用 logx.Delegating 懒解析。
func TestNoPrintfLogCallback(t *testing.T) {
	const repoRoot = "../.." // go test 的 CWD = 本包目录
	const rootPrefix = repoRoot + "/"
	var hits []string
	walkErr := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".hz", "node_modules", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		display := strings.TrimPrefix(path, rootPrefix)
		var importsStdlibLog bool
		var callHits []string
		for i, line := range strings.Split(string(data), "\n") {
			if printfCallbackRe.MatchString(line) {
				hits = append(hits, fmt.Sprintf("%s:%d printf 风格日志回调签名：留痕改走 logx.Logger（SDK 包级用 logx.Delegating）", display, i+1))
			}
			if stdlibLogImportRe.MatchString(line) {
				importsStdlibLog = true
				hits = append(hits, fmt.Sprintf("%s:%d stdlib log 包 import：留痕统一 logx.Logger", display, i+1))
			}
			if stdlibLogCallRe.MatchString(line) {
				callHits = append(callHits, fmt.Sprintf("%s:%d stdlib log 直呼：留痕统一 logx.Logger", display, i+1))
			}
		}
		if importsStdlibLog {
			hits = append(hits, callHits...)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("printf-log 扫描失败: %v", walkErr)
	}
	for _, h := range hits {
		t.Errorf("argus-no-printf-log-callback（error）: %s", h)
	}
}
