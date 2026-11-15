// client.go —— argus-copilot 的 MCP 连接/调用与输出出口（自 main.go 拆出，
// 行为不变；main.go 只留入口与子命令表）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/oekit/pkg/cli"
)

// connect 懒连接：用法/帮助路径不要求 argus 在位（此前 dial 先于分发，
// 服务器不可达时连 usage 都看不到）。
func connect(ctx context.Context, g globals, stderr io.Writer) (*mcpclient.Client, int) {
	c, err := dial(ctx, g.url, g.token)
	if err != nil {
		fmt.Fprintln(stderr, "argus-copilot:", err)
		return nil, cli.ExitError
	}
	return c, cli.ExitOK
}

// emit 工具命令统一出口：err → ExitError(2)；review/poll 的业务退出码
// （0/1/4）原样透传；成功输出走 stdout。
func emit(stdout, stderr io.Writer, out string, code int, err error) int {
	if err != nil {
		fmt.Fprintln(stderr, "argus-copilot:", err)
		return cli.ExitError
	}
	if out != "" && stdout != nil {
		fmt.Fprintln(stdout, out)
	}
	return code
}

// emitCall 只读工具（call）的出口：成功即 ExitOK。
func emitCall(stdout, stderr io.Writer, out string, err error) int {
	return emit(stdout, stderr, out, cli.ExitOK, err)
}

func dial(ctx context.Context, url, token string) (*mcpclient.Client, error) {
	c, err := mcpclient.NewStreamableHttpClient(url,
		mcptransport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		return nil, err
	}
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "argus-copilot", Version: "1"}
	if _, err := c.Initialize(ctx, req); err != nil {
		return nil, err
	}
	return c, nil
}

func call(ctx context.Context, cli *mcpclient.Client, name string, args map[string]any) (string, error) {
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		return "", err
	}
	if len(res.Content) == 0 {
		return "", fmt.Errorf("工具 %s 无内容", name)
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		b, _ := json.Marshal(res.Content[0])
		return string(b), nil
	}
	return tc.Text, nil
}

// splitPR platform/owner/repo#number → 四元组。
func splitPR(s string) (platform, owner, repo, number string, err error) {
	i := strings.IndexByte(s, '#')
	if i <= 0 {
		return "", "", "", "", fmt.Errorf("格式须为 platform/owner/repo#number: %q", s)
	}
	number = s[i+1:]
	parts := strings.Split(s[:i], "/")
	if len(parts) != 3 {
		return "", "", "", "", fmt.Errorf("格式须为 platform/owner/repo#number: %q", s)
	}
	return parts[0], parts[1], parts[2], number, nil
}
