// models_egress.go —— T7：LLM 出站掩码 + 哈希审计的 Generate 包装。
package bootstrap

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/egress"
)

// egressAuditGen 包装 llm.Generator：出站前对**审计副本** MaskCredentials +
// AuditLLM（哈希清单，零原文）。不改传给模型的内容（DataFence 语义不变）。
type egressAuditGen struct {
	inner llm.Generator
	log   logx.Logger
}

func (g egressAuditGen) audit(stage string, msgs []*schema.Message) {
	if g.inner == nil {
		return
	}
	var b strings.Builder
	for _, m := range msgs {
		if m != nil {
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
	}
	egress.AuditLLM(g.log, stage, []byte(egress.MaskCredentials(b.String())))
}

func (g egressAuditGen) Generate(ctx context.Context, stage string, msgs []*schema.Message) (*schema.Message, error) {
	if g.inner == nil {
		return nil, errors.New("llm: generator not ready")
	}
	g.audit(stage, msgs)
	return g.inner.Generate(ctx, stage, msgs)
}

func (g egressAuditGen) GenerateJSON(ctx context.Context, stage string, msgs []*schema.Message, out any) error {
	if g.inner == nil {
		return errors.New("llm: generator not ready")
	}
	g.audit(stage, msgs)
	return g.inner.GenerateJSON(ctx, stage, msgs, out)
}

func (g egressAuditGen) UsedTokens() int {
	if g.inner == nil {
		return 0
	}
	return g.inner.UsedTokens()
}

func (g egressAuditGen) RawModel() model.BaseChatModel {
	if g.inner == nil {
		return nil
	}
	return g.inner.RawModel()
}

// wrapEgressAudit 给 Generate 链挂掩码审计（nil 安全）。
func wrapEgressAudit(g llm.Generator, log logx.Logger) llm.Generator {
	if g == nil {
		return nil
	}
	return egressAuditGen{inner: g, log: log}
}
