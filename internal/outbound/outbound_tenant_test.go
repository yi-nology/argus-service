package outbound

import "testing"

// TestCheckSemiTrustedForTenant 租户附加白名单（#96）：命中豁免名字层（地址族
// 复核仍生效——公网字面量过、链路本地不放行），未命中回落全局策略。
// 全部用 IP 字面量：resolveHost 对字面量零 DNS（沙箱无网防挂）。
func TestRegisterTenantAllowEmptyTID(t *testing.T) {
	RegisterTenantAllow("", []string{"8.8.8.8"}) // 空 tid 早退不 panic
}

func TestCheckSemiTrustedForTenant(t *testing.T) {
	Configure(ModeEnforce, nil, nil)
	t.Cleanup(func() { Configure(ModeWarn, nil, nil) })
	const public = "93.184.216.34" // 公网字面量（示例段，仅形态用）
	RegisterTenantAllow("tenant-a", []string{public})
	// 租户附加白名单内的公网地址放行（名字层豁免 + 地址族复核通过）。
	if err := CheckSemiTrustedFor("tenant-a", "https://"+public+"/x"); err != nil {
		t.Fatalf("租户白名单内地址应放行: %v", err)
	}
	// 租户白名单不放行链路本地（地址族复核仍生效）。
	if err := CheckSemiTrustedFor("tenant-a", "http://169.254.169.254/latest"); err == nil {
		t.Fatal("租户白名单不放行元数据地址")
	}
	// 私网地址在各租户/全局下均拒绝（不泄漏放行语义）。
	if err := CheckSemiTrustedFor("tenant-b", "http://169.254.169.254/"); err == nil {
		t.Fatal("tenant-b 的元数据访问应拒绝")
	}
	if err := CheckSemiTrustedFor("", "http://169.254.169.254/"); err == nil {
		t.Fatal("全局 enforce 应拒绝元数据地址")
	}
	// 重复注册覆盖（后写生效）。
	RegisterTenantAllow("tenant-a", []string{"8.8.8.8"})
	if err := CheckSemiTrustedFor("tenant-a", "https://8.8.8.8/"); err != nil {
		t.Fatalf("覆盖后新白名单应生效: %v", err)
	}
}
