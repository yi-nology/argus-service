// service_impl_writing.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/obs"
)

// runWritingCommand 的包级实现（转发壳见原文件）。
func runWritingCommand(s *Service, ctx context.Context, platform, owner, repo, number,
	prRef, delivery string, spec writingSpec) {
	mdl := s.replyModel()
	if mdl == nil {
		return
	}
	diffText, ok := s.prDiffText(ctx, platform, owner, repo, number)
	if !ok {
		s.postQAError(ctx, platform, owner, repo, number, spec.diffFailText)
		return
	}
	sys, user, failMsg := spec.build(diffText)
	if failMsg != "" {
		s.postQAError(ctx, platform, owner, repo, number, failMsg)
		return
	}
	client := llm.NewClient(mdl, "", nil)
	resp, err := client.Generate(ctx, strings.ToUpper(spec.cmd), []*schema.Message{
		schema.SystemMessage(sys),
		schema.UserMessage(user),
	})
	if err != nil {
		obs.IncrQAFailure("llm")
		s.log().Error("argus."+spec.cmd+".llm_failed", "pr", prRef, "error", err.Error())
		s.postQAError(ctx, platform, owner, repo, number, spec.failText+": "+err.Error())
		return
	}
	out := strings.TrimSpace(resp.Content)
	if t, truncated := textutil.TruncRunes(out, spec.maxRunes); truncated {
		out = t + "\n…（截断）"
	}
	if spec.decorate != nil {
		out = spec.decorate(out)
	}
	body := out + "\n\n<!-- argus:" + spec.cmd + " delivery=" + delivery + " -->"
	s.postCommentReply(ctx, platform, owner, repo, number, prRef, body, spec.cmd, len(out))
}
