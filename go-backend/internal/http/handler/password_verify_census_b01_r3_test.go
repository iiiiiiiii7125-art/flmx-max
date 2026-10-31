package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// R3 B01-001 回归（枚举驱动全集门）：在线密码校验面全集以
// security.VerifyPassword 的非测试调用点机械枚举——凡承载调用的
// 函数必须在函数体内带限流守卫（checkLoginRateLimit 或 sub_store
// 的独立 subStoreLimiter），或在下方命名豁免清单内。新增密码校验
// 端点漏守卫即红，不依赖人工清单同步。清单文档见
// lanes/K3/r3/enumeration.md。

var passwordVerifyGuardExemptions = map[string]string{
	// 内部默认口令判定：入参是已鉴权路径取到的存储哈希与两个固定
	// 候选默认值的比较，不是提交口令的在线猜测面；其调用方
	// （login、passkey 登录）本身都在限流之后。
	"isStoredDefaultPassword": "internal default-password probe, not an online guessing surface",
}

// 预期在册的真校验面（删除任一调用点亦应被察觉，防止普查面静默缩水）。
var passwordVerifyExpectedFaces = []string{
	"login",
	"openAPISubStore",
	"passkeyRegisterBegin",
	"updatePassword",
	"passkeyDelete",
}

func passwordVerifyCensus(t *testing.T) map[string][2]bool {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	census := map[string][2]bool{} // funcName -> {callsVerify, hasGuard}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range node.Decls {
			fn, isFn := decl.(*ast.FuncDecl)
			if !isFn || fn.Body == nil {
				continue
			}
			var callsVerify, hasGuard bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				sel, isSel := call.Fun.(*ast.SelectorExpr)
				if !isSel {
					return true
				}
				if x, isIdent := sel.X.(*ast.Ident); isIdent {
					if x.Name == "security" && sel.Sel.Name == "VerifyPassword" {
						callsVerify = true
					}
					if x.Name == "subStoreLimiter" && sel.Sel.Name == "allow" {
						hasGuard = true
					}
				}
				if sel.Sel.Name == "checkLoginRateLimit" {
					hasGuard = true
				}
				return true
			})
			if callsVerify {
				census[fn.Name.Name] = [2]bool{true, hasGuard}
			}
		}
	}
	return census
}

func TestB01R3PasswordVerifyCensusAllFacesGuarded(t *testing.T) {
	census := passwordVerifyCensus(t)
	if len(census) == 0 {
		t.Fatal("census empty: enumeration failed to find any VerifyPassword call site")
	}
	for _, face := range passwordVerifyExpectedFaces {
		if _, ok := census[face]; !ok {
			t.Fatalf("expected password-verify face %q missing from census %v", face, census)
		}
	}
	for fn, flags := range census {
		if _, exempt := passwordVerifyGuardExemptions[fn]; exempt {
			continue
		}
		if !flags[1] {
			t.Fatalf("function %s calls security.VerifyPassword without a rate-limit guard (B01-001 census gate)", fn)
		}
	}
}

// 行为面：两条新补限流的校验面各连试 25 次错误密码，前 10 次走
// 正常校验回「当前密码错误」，第 11 次起 429（与 login 同口径）。
func TestB01R3UpdatePasswordRateLimited(t *testing.T) {
	resetLoginLimitersForTest()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	hash, err := security.HashPassword("old-pass-1")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := r.CreateUser("k3-updatepw", hash, 1, now+86400000, 100, 1, 10, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const ip = "198.51.100.241:4000"
	body := `{"newUsername":"k3-updatepw","currentPassword":"wrong-pass","newPassword":"new-pass-9","confirmPassword":"new-pass-9"}`
	for i := 1; i <= 25; i++ {
		w := httptest.NewRecorder()
		h.updatePassword(w, claimsCtxReq(http.MethodPost, "/api/v1/user/updatePassword", ip, body, uid, 1))
		env := decodeEnvelope(t, w)
		if i <= 10 && env.Code == 429 {
			t.Fatalf("attempt %d: rate limited too early", i)
		}
		if i == 10 && env.Code != -1 {
			t.Fatalf("attempt 10: want code=-1 (wrong current password), got %d (%s)", env.Code, env.Msg)
		}
		if i >= 11 && env.Code != 429 {
			t.Fatalf("attempt %d: want 429, got %d (%s)", i, env.Code, env.Msg)
		}
	}
}

func TestB01R3PasskeyDeleteRateLimited(t *testing.T) {
	resetLoginLimitersForTest()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	hash, err := security.HashPassword("old-pass-1")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := r.CreateUser("k3-passkeydel", hash, 1, now+86400000, 100, 1, 10, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const ip = "198.51.100.242:4000"
	body := `{"id":"cred-1","password":"wrong-pass"}`
	for i := 1; i <= 25; i++ {
		w := httptest.NewRecorder()
		h.passkeyDelete(w, claimsCtxReq(http.MethodPost, "/api/v1/user/passkey/delete", ip, body, uid, 1))
		env := decodeEnvelope(t, w)
		if i <= 10 && env.Code == 429 {
			t.Fatalf("attempt %d: rate limited too early", i)
		}
		if i == 10 && env.Code != -1 {
			t.Fatalf("attempt 10: want code=-1 (wrong password), got %d (%s)", env.Code, env.Msg)
		}
		if i >= 11 && env.Code != 429 {
			t.Fatalf("attempt %d: want 429, got %d (%s)", i, env.Code, env.Msg)
		}
	}
}

// 防清单漂移的自检：豁免清单里的函数必须真实存在且确实承载调用，
// 否则豁免条目已成陈旧掩护。
func TestB01R3PasswordVerifyCensusExemptionsAreReal(t *testing.T) {
	census := passwordVerifyCensus(t)
	for fn := range passwordVerifyGuardExemptions {
		if _, ok := census[fn]; !ok {
			t.Fatalf("exemption %q no longer calls VerifyPassword; remove the stale exemption", fn)
		}
	}
}
