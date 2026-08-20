package services

import (
	"strings"
	"testing"

	"github.com/enterprise-kb/backend/internal/config"
)

func newTestAuth() *AuthService {
	return NewAuthService(config.JWTConfig{})
}

func TestValidatePassword(t *testing.T) {
	s := newTestAuth()

	cases := []struct {
		name string
		pw   string
		ok   bool
	}{
		{"too short", "abc123", false},
		{"letters only", "abcdefgh", false},
		{"digits only", "12345678", false},
		{"valid", "demo1234", true},
		{"valid with symbols", "Passw0rd!#", true},
		{"empty", "", false},
		{"bordering length", "1234567a", true}, // 8 chars, letter+digit
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.validatePassword(c.pw)
			if c.ok && err != nil {
				t.Errorf("password %q should pass, got %v", c.pw, err)
			}
			if !c.ok && err == nil {
				t.Errorf("password %q should fail", c.pw)
			}
		})
	}
}

func TestValidatePassword_CustomMinLen(t *testing.T) {
	s := newTestAuth()
	s.Configure(true, 10, 5, 15)
	if err := s.validatePassword("demo1234"); err == nil {
		t.Errorf("8-char password should fail with min length 10")
	}
	if err := s.validatePassword("demo123456"); err != nil {
		t.Errorf("10-char alnum password should pass, got %v", err)
	}
}

func TestSlugify(t *testing.T) {
	s := slugify("星辰科技有限公司")
	if !strings.HasSuffix(s, "-") && len(s) == 0 {
		t.Errorf("slug should be non-empty, got %q", s)
	}
	// oracle: ascii slugs keep chars
	if got := slugify("ACME Corp 2026"); !strings.HasPrefix(got, "acme-corp-2026") {
		t.Errorf("ascii slug prefix mismatch: %q", got)
	}
	if got := slugify("   "); got != "org" && !strings.HasPrefix(got, "org") {
		t.Errorf("whitespace slug should default, got %q", got)
	}
}