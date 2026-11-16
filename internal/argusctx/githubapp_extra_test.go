package argusctx

// GitHub App 认证面测试：JWT 签发/验签、installation token 交换（HTTP 用本地
// RoundTripper 打桩，绝不触网）、token source 续期与错误路径。

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubGitHubHTTP 替换包级 GitHub API 客户端（进程内假传输，零网络）。
func stubGitHubHTTP(t *testing.T, fn roundTripFunc) {
	t.Helper()
	old := githubAppHTTPClient
	githubAppHTTPClient = &http.Client{Transport: fn}
	t.Cleanup(func() { githubAppHTTPClient = old })
}

func httpJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
	testKeyPEM  string
)

func rsaTestKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
		testKeyPEM = string(pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(k),
		}))
	})
	return testKey, testKeyPEM
}

func TestSignJWTClaimsAndSignature(t *testing.T) {
	key, _ := rsaTestKey(t)
	now := time.Unix(1_700_000_000, 0)
	token, err := signJWT(4242, now, key)
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT 段数 = %d, want 3", len(parts))
	}
	dec := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("base64url 解码失败: %v", err)
		}
		return b
	}
	var header map[string]string
	if err := json.Unmarshal(dec(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Fatalf("header 错: %v", header)
	}
	var claims map[string]int64
	if err := json.Unmarshal(dec(parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != 4242 {
		t.Errorf("iss = %d, want 4242", claims["iss"])
	}
	if claims["iat"] != now.Unix()-10 {
		t.Errorf("iat = %d, want now-10s", claims["iat"])
	}
	if claims["exp"] != now.Add(9*time.Minute).Unix() {
		t.Errorf("exp = %d, want now+9m", claims["exp"])
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], dec(parts[2])); err != nil {
		t.Fatalf("签名验证失败: %v", err)
	}
}

func TestGenerateInstallationTokenPKCS1Success(t *testing.T) {
	_, keyPEM := rsaTestKey(t)
	var gotMethod, gotPath, gotAuth, gotAccept string
	stubGitHubHTTP(t, func(r *http.Request) (*http.Response, error) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth, gotAccept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		return httpJSONResponse(http.StatusCreated,
			`{"token":"ghs_test","expires_at":"2026-11-16T13:00:00Z"}`), nil
	})

	token, expires, err := generateInstallationToken(1, keyPEM, 77)
	if err != nil {
		t.Fatalf("generateInstallationToken: %v", err)
	}
	if token != "ghs_test" {
		t.Errorf("token = %q, want ghs_test", token)
	}
	if !expires.Equal(time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("expires = %v", expires)
	}
	if gotMethod != http.MethodPost || gotPath != "/app/installations/77/access_tokens" {
		t.Errorf("请求 = %s %s", gotMethod, gotPath)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") || gotAccept != "application/vnd.github+json" {
		t.Errorf("认证/接受头错: %q %q", gotAuth, gotAccept)
	}
}

func TestGenerateInstallationTokenPKCS8Success(t *testing.T) {
	key, _ := rsaTestKey(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	stubGitHubHTTP(t, func(*http.Request) (*http.Response, error) {
		return httpJSONResponse(http.StatusCreated,
			`{"token":"pkcs8","expires_at":"2026-11-16T13:00:00Z"}`), nil
	})
	token, _, err := generateInstallationToken(1, keyPEM, 2)
	if err != nil || token != "pkcs8" {
		t.Fatalf("PKCS8 私钥应可用: token=%q err=%v", token, err)
	}
}

func TestGenerateInstallationTokenKeyErrors(t *testing.T) {
	rsaTestKey(t) // 预热共享密钥
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecPKCS8, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	ecPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecPKCS8}))
	badBlockPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte{1, 2, 3}}))
	noBlock := base64.StdEncoding.EncodeToString([]byte("hello"))

	cases := []struct {
		name, key, want string
	}{
		{"bad base64", "!!!not-base64", "base64 解码私钥失败"},
		{"no PEM block", noBlock, "PEM 解析失败"},
		{"garbage block", badBlockPEM, "解析私钥失败"},
		{"non-RSA key", ecPEM, "非 RSA 私钥"},
	}
	for _, c := range cases {
		_, _, err := generateInstallationToken(1, c.key, 1)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want 含 %q", c.name, err, c.want)
		}
	}
}

func TestGenerateInstallationTokenAPIErrors(t *testing.T) {
	_, keyPEM := rsaTestKey(t)
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"message":"bad credentials"}`, "返回 401"},
		{"bad json", http.StatusCreated, `not-json`, "解析响应失败"},
		{"empty token", http.StatusCreated, `{"token":"","expires_at":"2026-11-16T13:00:00Z"}`, "空 token"},
	}
	for _, c := range cases {
		stubGitHubHTTP(t, func(*http.Request) (*http.Response, error) {
			return httpJSONResponse(c.status, c.body), nil
		})
		_, _, err := generateInstallationToken(1, keyPEM, 1)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want 含 %q", c.name, err, c.want)
		}
	}
}

func TestNewInstallationTokenSourceMapsConfig(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	ts := newInstallationTokenSource(config.PlatformConfig{
		GitHubAppID: 11, GitHubAppPrivateKey: "pem", GitHubAppInstallationID: 22,
	}, "tok", expires, logx.Nop())
	if ts.appID != 11 || ts.privateKeyPEM != "pem" || ts.installationID != 22 {
		t.Fatalf("字段映射错: %+v", ts)
	}
	if ts.token != "tok" || !ts.expiresAt.Equal(expires) {
		t.Fatalf("初始 token 错: %q %v", ts.token, ts.expiresAt)
	}
}

func TestInstallationTokenSourceRefreshesOnExpiry(t *testing.T) {
	_, keyPEM := rsaTestKey(t)
	var calls int
	stubGitHubHTTP(t, func(*http.Request) (*http.Response, error) {
		calls++
		return httpJSONResponse(http.StatusCreated,
			`{"token":"fresh","expires_at":"2026-11-16T13:00:00Z"}`), nil
	})
	ts := newInstallationTokenSource(config.PlatformConfig{
		GitHubAppID: 1, GitHubAppPrivateKey: keyPEM, GitHubAppInstallationID: 3,
	}, "stale", time.Now().Add(-time.Minute), logx.Nop())

	got, err := ts.Token(context.Background())
	if err != nil || got != "fresh" {
		t.Fatalf("过期 token 应刷新: token=%q err=%v", got, err)
	}
	if calls != 1 {
		t.Fatalf("刷新请求数 = %d, want 1", calls)
	}
	// 刷新后寿命充裕：第二次取值直接命中缓存。
	got, err = ts.Token(context.Background())
	if err != nil || got != "fresh" || calls != 1 {
		t.Fatalf("刷新后应命中缓存: token=%q calls=%d err=%v", got, calls, err)
	}
}

func TestInstallationTokenSourceNoCredsFails(t *testing.T) {
	ts := &installationTokenSource{log: logx.Nop()}
	if _, err := ts.Token(context.Background()); err == nil {
		t.Fatal("无初始 token 且无凭据应报错")
	}
}
