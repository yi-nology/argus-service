package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestTrustedDialHappyEyeballs 两个解析结果并发竞速：一个失败一个成功 →
// 成功连接胜出且保持打开。
func TestTrustedDialHappyEyeballs(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("10.0.0.1"),
			netip.MustParseAddr("10.0.0.2"),
		}, nil
	}

	win := newCountingConn("10.0.0.2")
	got, err := trustedDial(context.Background(), "tcp", "gw.internal:80",
		func(_ context.Context, _, addr string) (net.Conn, error) {
			if addr == "10.0.0.1:80" {
				return nil, errors.New("first address unreachable")
			}
			return win, nil
		})
	if err != nil {
		t.Fatalf("一个地址失败时另一地址应胜出: %v", err)
	}
	if got != win {
		t.Fatalf("应返回成功地址的连接，got %v", got)
	}
	if win.isClosed() {
		t.Fatal("胜出连接不得被收尾逻辑关闭")
	}
}

// TestTrustedDialHappyEyeballsAllFail 全部地址失败 → 返回错误（错误链保留
// 底层拨号错误）。
func TestTrustedDialHappyEyeballsAllFail(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("10.0.0.1"),
			netip.MustParseAddr("10.0.0.2"),
		}, nil
	}

	errA, errB := errors.New("dial a failed"), errors.New("dial b failed")
	_, err := trustedDial(context.Background(), "tcp", "gw.internal:80",
		func(_ context.Context, _, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "10.0.0.1:") {
				return nil, errA
			}
			return nil, errB
		})
	if err == nil {
		t.Fatal("全部地址失败应报错")
	}
	if !errors.Is(err, errA) && !errors.Is(err, errB) {
		t.Fatalf("应返回最后收到的底层拨号错误，实得 %v", err)
	}
}

// TestTrustedDialHappyEyeballsClosesLateSuccess 胜出后晚到的成功连接必须关闭
// （否则取消其余拨号会泄漏 fd）。
func TestTrustedDialHappyEyeballsClosesLateSuccess(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("10.0.0.1"),
			netip.MustParseAddr("10.0.0.2"),
		}, nil
	}

	release := make(chan struct{})
	late := newCountingConn("10.0.0.1")
	win := newCountingConn("10.0.0.2")
	got, err := trustedDial(context.Background(), "tcp", "gw.internal:80",
		func(_ context.Context, _, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "10.0.0.1:") {
				<-release // 胜出者返回后才完成，模拟慢地址
				return late, nil
			}
			return win, nil
		})
	if err != nil || got != win {
		t.Fatalf("快速地址应胜出: conn=%v err=%v", got, err)
	}
	if late.isClosed() {
		t.Fatal("晚到连接在返回前不应被关闭")
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for !late.isClosed() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !late.isClosed() {
		t.Fatal("胜出后晚到的成功连接必须关闭（避免泄漏）")
	}
}

// countingConn 并发安全的可观测连接（RemoteAddr 用于建连后核对）。
type countingConn struct {
	fakeConn
	closedFlag atomic.Bool
}

func newCountingConn(ip string) *countingConn {
	return &countingConn{fakeConn: fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(ip)}}}
}

func (c *countingConn) Close() error { c.closedFlag.Store(true); return nil }

func (c *countingConn) isClosed() bool { return c.closedFlag.Load() }
