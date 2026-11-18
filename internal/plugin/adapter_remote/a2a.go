// a2a.go remote 插件的 A2A client 子集（design §3.2）。
// 流程 = Agent Card 发现（/.well-known/agent.json）→ message/send 提交 →
// tasks/get 轮询至终态 → artifact 文本解析 findings。
// 只实现 A2A 协议中审查委托所需的子集（非流式、单工件），不做协商/推送（§8）。

package adapter_remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/oekit/kitx/uidgen"

	"github.com/yi-nology/argus/internal/outbound"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/tenant"
)

// A2A 任务终态词表（spec: TaskState）。
const (
	a2aStateCompleted = "completed"
	a2aStateFailed    = "failed"
	a2aStateCanceled  = "canceled"
	a2aStateInputReq  = "input-required"
)

const a2aPollInterval = 2 * time.Second

type a2aAgentCard struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type a2aPart struct {
	Kind string `json:"kind"` // "text"
	Text string `json:"text"`
}

type a2aArtifact struct {
	Name  string    `json:"name,omitempty"`
	Parts []a2aPart `json:"parts"`
}

type a2aTask struct {
	ID     string `json:"id"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
	Artifacts []a2aArtifact `json:"artifacts,omitempty"`
}

// invokeA2A 审查委托全流程。endpoint 为 agent 基地址（卡片的 /.well-known 前缀）。
func (r *Remote) invokeA2A(ctx context.Context, task plugin.Task, workcopyDir string) (plugin.Result, error) {
	_, payload, err := buildPayload(task, workcopyDir)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("构造 payload 失败: %w", err)
	}
	// 半可信出站守卫：Endpoint 来自 repo 可达配置，远端 card.URL 更是远端输入。
	client := outbound.SemiTrustedClient(http.DefaultClient)
	serviceURL, err := r.a2aDiscover(ctx, client, tenant.FromContext(ctx))
	if err != nil {
		return plugin.Result{}, err
	}
	t, err := r.a2aSend(ctx, client, serviceURL, payload)
	if err != nil {
		return plugin.Result{}, err
	}
	t, err = r.a2aPoll(ctx, client, serviceURL, t.ID)
	if err != nil {
		return plugin.Result{}, err
	}
	var texts []string
	for _, a := range t.Artifacts {
		for _, p := range a.Parts {
			if p.Kind == "text" {
				texts = append(texts, p.Text)
			}
		}
	}
	return parseFindings(textFromContents(texts))
}

// a2aDiscover 拉取 Agent Card；card.url（服务真实地址）优先于 endpoint。
// Endpoint 与 card.URL 都是半可信输入：发现前校验 Endpoint，card.URL 返回后、
// 使用前必须再校验（恶意仓库可让远端把 argus 指向内网/元数据地址）。
func (r *Remote) a2aDiscover(ctx context.Context, client *http.Client, tid string) (string, error) {
	if err := outbound.CheckSemiTrustedFor(tid, trimRight(r.Endpoint)); err != nil {
		return "", fmt.Errorf("A2A Endpoint 被出站策略拒绝: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		trimRight(r.Endpoint)+"/.well-known/agent.json", nil)
	if err != nil {
		return "", err
	}
	var card a2aAgentCard
	if err := a2aDoJSON(client, req, &card); err != nil {
		return "", fmt.Errorf("A2A Agent Card 发现失败: %w", err)
	}
	return a2aServiceURL(card.URL, trimRight(r.Endpoint), tid)
}

// a2aServiceURL 决定后续 RPC 的服务地址：card.url 非空时以它为链上的新起点，
// 必须先过半可信校验；为空时回退到已校验的 endpoint。
func a2aServiceURL(cardURL, endpoint, tid string) (string, error) {
	if cardURL == "" {
		return endpoint, nil
	}
	if err := outbound.CheckSemiTrustedFor(tid, cardURL); err != nil {
		return "", fmt.Errorf("A2A Agent Card 返回的 url 被出站策略拒绝: %w", err)
	}
	return cardURL, nil
}

// a2aSend message/send 提交任务。
func (r *Remote) a2aSend(ctx context.Context, client *http.Client, serviceURL, payload string) (*a2aTask, error) {
	msg := map[string]any{
		"role":      "ROLE_USER",
		"kind":      "message",
		"messageId": a2aMessageID(),
		"parts":     []a2aPart{{Kind: "text", Text: payload}},
	}
	var out a2aTask
	if err := r.a2aRPC(ctx, client, serviceURL, "message/send", map[string]any{"message": msg}, &out); err != nil {
		return nil, fmt.Errorf("A2A message/send 失败: %w", err)
	}
	if out.ID == "" {
		return nil, fmt.Errorf("A2A 响应缺少 task id")
	}
	return &out, nil
}

// a2aPoll tasks/get 轮询至终态（含同步完成的首次响应）。
func (r *Remote) a2aPoll(ctx context.Context, client *http.Client, serviceURL, taskID string) (*a2aTask, error) {
	for {
		var t a2aTask
		if err := r.a2aRPC(ctx, client, serviceURL, "tasks/get", map[string]any{"id": taskID}, &t); err != nil {
			return nil, fmt.Errorf("A2A tasks/get 失败: %w", err)
		}
		switch t.Status.State {
		case a2aStateCompleted:
			return &t, nil
		case a2aStateFailed, a2aStateCanceled:
			return nil, fmt.Errorf("A2A 任务 %s 终态 %s", taskID, t.Status.State)
		case a2aStateInputReq:
			return nil, fmt.Errorf("A2A 任务 %s 需要输入，无法自动继续", taskID)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("A2A 任务 %s 轮询超时", taskID)
		case <-time.After(a2aPollInterval):
		}
	}
}

// a2aRPC 单次 JSON-RPC 2.0 请求（method/params → result）。
func (r *Remote) a2aRPC(ctx context.Context, client *http.Client, serviceURL, method string, params any, out any) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": a2aMessageID(), "method": method, "params": params,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	var wrapped struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := a2aDoJSON(client, req, &wrapped); err != nil {
		return err
	}
	if wrapped.Error != nil {
		return fmt.Errorf("rpc error %d: %s", wrapped.Error.Code, wrapped.Error.Message)
	}
	if len(wrapped.Result) == 0 {
		return fmt.Errorf("A2A 响应缺少 result")
	}
	return json.Unmarshal(wrapped.Result, out)
}

// a2aDoJSON 统一走出站守卫后的 client（拨号层守卫 + 逐跳重定向复验）。
// nil client 是装配错误：显式报错，绝不回退到无守卫的 http.DefaultClient。
func a2aDoJSON(client *http.Client, req *http.Request, out any) error {
	if client == nil {
		return fmt.Errorf("A2A 请求缺少出站守卫 client")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // 解码失败的 body 关闭错误无处置意义
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// a2aMessageID v3.82.0 引入 ekit uidgen.ShortUUID（16 位短 ID；旧实现
// crypto/rand 静默忽略读取错误，弱随机时产出重复 ID）。
func a2aMessageID() string {
	return "argus-" + uidgen.ShortUUID()
}

func trimRight(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
