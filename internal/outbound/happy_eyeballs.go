// happy_eyeballs.go —— 可信档域名拨号的并发竞速（Happy Eyeballs）。
//
// 解析结果已经逐个过 checkTrustedIP（任一硬拒即整名拒绝）；本文件只负责在
// 剩余地址之间竞速：第一个成功建连并核对 RemoteAddr 的地址胜出，其余取消，
// 晚到的成功连接关闭。全部失败返回最后收到的错误；外部 ctx 取消优先。

package outbound

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/oekit/concurrency/async"
)

// dialOutcome 竞速中单个 IP 的拨号结果（每个参与者只写一次）。
type dialOutcome struct {
	conn net.Conn
	err  error
}

// raceDial 对已校验的 IP 并发竞速（Happy Eyeballs）：多个地址同时拨，第一个
// 成功连接胜出并取消其余；晚到的成功连接由收尾协程关闭，避免 fd 泄漏。
// 全部失败返回最后收到的错误；外部 ctx 取消优先于任何结果。
func raceDial(ctx context.Context, network, port string, ips []netip.Addr,
	next func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, fmt.Errorf("可信出站目标无可拨号地址")
	}
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 容量=参与者数：参与者写结果永不阻塞，收尾协程可按剩余数精确收口。
	results := make(chan dialOutcome, len(ips))
	for _, ip := range ips {
		ip := ip
		addr := net.JoinHostPort(ip.String(), port)
		async.GoSafe(func() {
			// 每个参与者必须写一次结果（panic 也一样）：否则调用方按人数收口时
			// 会少一份而挂住。panic 由 GoSafe 收编，这里只需给出可归因的失败。
			outcome := dialOutcome{err: fmt.Errorf("可信拨号协程异常退出（%s）", addr)}
			defer func() { results <- outcome }()
			conn, err := dialAndVerify(raceCtx, network, addr, ip, next)
			outcome = dialOutcome{conn: conn, err: err}
		})
	}

	var lastErr error
	for received := 0; received < len(ips); received++ {
		select {
		case <-raceCtx.Done():
			// 外部取消：未读结果里可能还有晚到的成功连接，收尾协程逐个关闭。
			closeLateConns(results, len(ips)-received)
			return nil, raceCtx.Err()
		case r := <-results:
			if r.err != nil {
				lastErr = r.err
				continue
			}
			// ctx 取消优先：取消与成功同时就绪时不把连接交给调用方。
			if err := ctx.Err(); err != nil {
				_ = r.conn.Close()
				cancel()
				closeLateConns(results, len(ips)-received-1)
				return nil, err
			}
			cancel()
			closeLateConns(results, len(ips)-received-1)
			return r.conn, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("可信出站目标无可拨号地址")
	}
	return nil, lastErr
}

// closeLateConns 收尾竞速取消后仍可能到达的结果：晚到的成功连接立即关闭。
// n 是尚未读取的结果数（每个参与者只写一次，故读满 n 次即收口）。
func closeLateConns(results <-chan dialOutcome, n int) {
	if n <= 0 {
		return
	}
	async.GoSafe(func() {
		for range n {
			if r := <-results; r.conn != nil {
				_ = r.conn.Close()
			}
		}
	})
}
