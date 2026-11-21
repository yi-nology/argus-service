// auth_user.go —— 多用户最小集（T5）：用户、邀请、会话。
//
// 角色 → 既有 Bearer scope 映射（不破坏存量 token 三档）：
//
//	viewer    → read
//	operator  → ci
//	maintainer→ admin（可审批/合并/推送——业务裁定面）
//	admin     → admin（实例管理 + 邀请）
//	auditor   → read（审计读走 admin 档 token 或后续扩 scope；先投影只读）
//
// 登录签发会话 token（随机 32 字节 hex，存哈希）；BearerMiddleware 先查
// 配置 token，再查会话。邀请由 admin 创建，被邀请人 accept 时建用户。
package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// UserRole 角色词表。
const (
	RoleViewer     = "viewer"
	RoleOperator   = "operator"
	RoleMaintainer = "maintainer"
	RoleAdmin      = "admin"
	RoleAuditor    = "auditor"
)

// ValidRole 角色是否在词表内。
func ValidRole(r string) bool {
	switch r {
	case RoleViewer, RoleOperator, RoleMaintainer, RoleAdmin, RoleAuditor:
		return true
	}
	return false
}

// RoleToScope 角色 → Bearer scope（read|ci|admin）。
func RoleToScope(role string) string {
	switch role {
	case RoleOperator:
		return "ci"
	case RoleMaintainer, RoleAdmin:
		return "admin"
	default: // viewer / auditor / 未知
		return "read"
	}
}

// AuthStore 用户/邀请/会话读写面（挂同一 Store 连接；独立类型避免
// Store 方法数撞 god-object 阈值——与 SideEffectStore 同模式）。
type AuthStore struct{ *Store }

// Auths 返回挂在同一个库上的认证存储（零值可用）。
func (s *Store) Auths() *AuthStore { return &AuthStore{Store: s} }

// User 用户行。
type User struct {
	TenantID string `gorm:"column:tenant_id;size:64;not null;default:default;index"`
	ID       string `gorm:"column:id;primaryKey;size:40"`
	Username string `gorm:"column:username;size:64;not null;uniqueIndex:idx_users_name,priority:2"`
	Role     string `gorm:"column:role;size:16;not null"`
	// PasswordHash scrypt/sha256(salt+password) 十六进制（最小集用 sha256+salt；
	// 生产建议升级 argon2——见 docs 注释）。
	PasswordHash string    `gorm:"column:password_hash;size:128;not null"`
	Salt         string    `gorm:"column:salt;size:32;not null"`
	Disabled     bool      `gorm:"column:disabled;default:false"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

func (User) TableName() string { return "users" }

// Invite 邀请行（一次性）。
type Invite struct {
	TenantID  string     `gorm:"column:tenant_id;size:64;not null;default:default;index"`
	ID        string     `gorm:"column:id;primaryKey;size:40"`
	Role      string     `gorm:"column:role;size:16;not null"`
	TokenHash string     `gorm:"column:token_hash;size:64;not null;uniqueIndex"`
	Username  string     `gorm:"column:username;size:64"` // 可选预定用户名
	CreatedBy string     `gorm:"column:created_by;size:40"`
	ExpiresAt time.Time  `gorm:"column:expires_at;index"`
	UsedAt    *time.Time `gorm:"column:used_at"`
	UsedBy    string     `gorm:"column:used_by;size:40"`
	CreatedAt time.Time  `gorm:"column:created_at"`
}

func (Invite) TableName() string { return "invites" }

// Session 会话行（token 只存哈希）。
type Session struct {
	TenantID  string     `gorm:"column:tenant_id;size:64;not null;default:default;index"`
	ID        string     `gorm:"column:id;primaryKey;size:40"`
	UserID    string     `gorm:"column:user_id;size:40;not null;index"`
	TokenHash string     `gorm:"column:token_hash;size:64;not null;uniqueIndex"`
	Role      string     `gorm:"column:role;size:16;not null"` // 登录时角色快照
	ExpiresAt time.Time  `gorm:"column:expires_at;index"`
	CreatedAt time.Time  `gorm:"column:created_at"`
	RevokedAt *time.Time `gorm:"column:revoked_at"`
}

func (Session) TableName() string { return "sessions" }

// HashToken token → 存储哈希（sha256 hex）。
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// RandomToken 生成 32 字节随机 token 的 hex。
func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// RandomSalt 生成 16 字节 salt hex。
func RandomSalt() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HashPassword sha256(salt + ":" + password) hex（最小集；见包注释升级说明）。
func HashPassword(salt, password string) string {
	sum := sha256.Sum256([]byte(salt + ":" + password))
	return hex.EncodeToString(sum[:])
}

// CreateUser 建用户（username 唯一）。
func (s *AuthStore) CreateUser(ctx context.Context, u *User) error {
	return s.withWrite(func() error {
		if u.ID == "" {
			u.ID = "u-" + NewTaskID()[2:]
		}
		now := time.Now()
		u.CreatedAt, u.UpdatedAt = now, now
		return s.db.WithContext(ctx).Create(u).Error
	})
}

// GetUserByUsername 按用户名读用户。
func (s *AuthStore) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	err := s.db.WithContext(ctx).Where("username = ?", strings.TrimSpace(username)).First(&u).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// CreateInvite 建邀请，返回明文 token（仅此一次可见）。
func (s *AuthStore) CreateInvite(ctx context.Context, role, username, createdBy string, ttl time.Duration) (token string, inv *Invite, err error) {
	if !ValidRole(role) {
		return "", nil, errors.New("auth: 非法角色")
	}
	if role == RoleAdmin {
		// 与 MergePilot 同纪律：邀请永不授予平台管理员——admin 由部署者初始化。
		return "", nil, errors.New("auth: 邀请不能授予 admin 角色")
	}
	token, err = RandomToken()
	if err != nil {
		return "", nil, err
	}
	inv = &Invite{
		ID: "i-" + NewTaskID()[2:], Role: role, TokenHash: HashToken(token),
		Username: strings.TrimSpace(username), CreatedBy: createdBy,
		ExpiresAt: time.Now().Add(ttl), CreatedAt: time.Now(),
	}
	if inv.ID == "" {
		inv.ID = "i-" + NewTaskID()[2:]
	}
	err = s.withWrite(func() error {
		return s.db.WithContext(ctx).Create(inv).Error
	})
	if err != nil {
		return "", nil, err
	}
	return token, inv, nil
}

// AcceptInvite 校验邀请并建用户（事务内：标记 used + 建用户）。
func (s *AuthStore) AcceptInvite(ctx context.Context, token, username, password string) (*User, error) {
	if strings.TrimSpace(username) == "" || len(password) < 8 {
		return nil, errors.New("auth: 用户名非空且密码至少 8 位")
	}
	var user *User
	err := s.withWrite(func() error {
		var inv Invite
		if err := s.db.WithContext(ctx).Where("token_hash = ?", HashToken(token)).First(&inv).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("auth: 邀请无效")
			}
			return err
		}
		if inv.UsedAt != nil {
			return errors.New("auth: 邀请已使用")
		}
		if time.Now().After(inv.ExpiresAt) {
			return errors.New("auth: 邀请已过期")
		}
		if !ValidRole(inv.Role) {
			return errors.New("auth: 邀请角色非法")
		}
		salt, err := RandomSalt()
		if err != nil {
			return err
		}
		uname := strings.TrimSpace(username)
		if inv.Username != "" && inv.Username != uname {
			// 预定用户名不匹配：仍允许（管理员可不锁定）——不强制。
		}
		u := &User{
			ID: "u-" + NewTaskID()[2:], Username: uname, Role: inv.Role,
			PasswordHash: HashPassword(salt, password), Salt: salt,
		}
		if err := s.db.WithContext(ctx).Create(u).Error; err != nil {
			return err
		}
		now := time.Now()
		if err := s.db.WithContext(ctx).Model(&Invite{}).
			Where("id = ? AND used_at IS NULL", inv.ID).
			Updates(map[string]any{"used_at": now, "used_by": u.ID}).Error; err != nil {
			return err
		}
		user = u
		return nil
	})
	return user, err
}

// VerifyLogin 用户名密码校验；成功返回用户。
func (s *AuthStore) VerifyLogin(ctx context.Context, username, password string) (*User, error) {
	u, err := s.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, errors.New("auth: 账号已停用")
	}
	if HashPassword(u.Salt, password) != u.PasswordHash {
		return nil, errors.New("auth: 用户名或密码错误")
	}
	return u, nil
}

// CreateSession 开会话，返回明文 token。
func (s *AuthStore) CreateSession(ctx context.Context, userID, role string, ttl time.Duration) (token string, sess *Session, err error) {
	token, err = RandomToken()
	if err != nil {
		return "", nil, err
	}
	sess = &Session{
		ID: "s-" + NewTaskID()[2:], UserID: userID, TokenHash: HashToken(token),
		Role: role, ExpiresAt: time.Now().Add(ttl), CreatedAt: time.Now(),
	}
	err = s.withWrite(func() error {
		return s.db.WithContext(ctx).Create(sess).Error
	})
	if err != nil {
		return "", nil, err
	}
	return token, sess, nil
}

// GetSessionByToken 按明文 token 读有效会话（未过期、未撤销）；附带用户。
func (s *AuthStore) GetSessionByToken(ctx context.Context, token string) (*Session, *User, error) {
	var sess Session
	err := s.db.WithContext(ctx).Where("token_hash = ?", HashToken(token)).First(&sess).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	if sess.RevokedAt != nil || time.Now().After(sess.ExpiresAt) {
		return nil, nil, ErrNotFound
	}
	var u User
	if err := s.db.WithContext(ctx).Where("id = ?", sess.UserID).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &sess, nil, nil // 用户被删：会话悬挂，调用方拒
		}
		return nil, nil, err
	}
	if u.Disabled {
		return nil, nil, ErrNotFound
	}
	return &sess, &u, nil
}

// RevokeSession 撤销会话（登出）。
func (s *AuthStore) RevokeSession(ctx context.Context, token string) error {
	return s.withWrite(func() error {
		now := time.Now()
		return s.db.WithContext(ctx).Model(&Session{}).
			Where("token_hash = ? AND revoked_at IS NULL", HashToken(token)).
			Update("revoked_at", now).Error
	})
}

// SeedBootstrapAdmin 若用户表为空，创建首个 admin（用户名/密码来自参数）。
// 幂等：已有任何用户则跳过。
func (s *AuthStore) SeedBootstrapAdmin(ctx context.Context, username, password string) (created bool, err error) {
	if strings.TrimSpace(username) == "" || len(password) < 8 {
		return false, errors.New("auth: 引导 admin 需要用户名 + 至少 8 位密码")
	}
	var n int64
	if err := s.db.WithContext(ctx).Model(&User{}).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	salt, err := RandomSalt()
	if err != nil {
		return false, err
	}
	u := &User{
		ID: "u-" + NewTaskID()[2:], Username: strings.TrimSpace(username),
		Role: RoleAdmin, PasswordHash: HashPassword(salt, password), Salt: salt,
	}
	if err := s.CreateUser(ctx, u); err != nil {
		return false, err
	}
	return true, nil
}
