// assemble_auth.go —— T5 会话面冷启动：首个 admin 引导。
package bootstrap

import (
	"context"
	"os"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/storage"
)

// seedBootstrapAuth ARGUS_BOOTSTRAP_ADMIN_USER/PASS 创建首个 admin（幂等）。
// 任一为空或用户表已有数据则跳过。密码 <8 位告警并跳过（不半建）。
func seedBootstrapAuth(ctx context.Context, store *storage.Store, log logx.Logger) {
	if store == nil {
		return
	}
	user := os.Getenv("ARGUS_BOOTSTRAP_ADMIN_USER")
	pass := os.Getenv("ARGUS_BOOTSTRAP_ADMIN_USER_PASS")
	if pass == "" {
		pass = os.Getenv("ARGUS_BOOTSTRAP_ADMIN_PASS")
	}
	if user == "" || pass == "" {
		return
	}
	created, err := store.Auths().SeedBootstrapAdmin(ctx, user, pass)
	if err != nil {
		if log != nil {
			log.Warn("argus.boot.bootstrap_admin_failed", "error", err.Error())
		}
		return
	}
	if created && log != nil {
		log.Info("argus.boot.bootstrap_admin_created", "username", user)
	}
}
