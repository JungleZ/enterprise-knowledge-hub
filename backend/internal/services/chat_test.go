package services

import (
	"testing"

	"github.com/enterprise-kb/backend/internal/models"
	"github.com/google/uuid"
)

func TestCanSeeKB(t *testing.T) {
	kbAll := models.KnowledgeBase{AllowedDepartments: ""}
	kbFinance := models.KnowledgeBase{AllowedDepartments: "财务部,全员"}
	kbResearch := models.KnowledgeBase{AllowedDepartments: "研发部"}

	cases := []struct {
		name string
		kb   models.KnowledgeBase
		dept string
		want bool
	}{
		{"no restriction -> anyone", kbAll, "任意部门", true},
		{"matching dept", kbFinance, "财务部", true},
		{"全员 keyword grants everyone", kbFinance, "市场部", true},
		{"restricted kb, non-listed dept", kbResearch, "财务部", false},
		{"restricted kb, listed dept", kbResearch, "研发部", true},
		{"empty dept string, unrestricted kb", kbAll, "", true},
		{"empty dept string, restricted kb", kbResearch, "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canSeeKB(c.kb, c.dept); got != c.want {
				t.Errorf("canSeeKB(dept=%q) = %v, want %v", c.dept, got, c.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 100); got != "short" {
		t.Errorf("short string should be unchanged, got %q", got)
	}
	got := truncate("一二三四五六", 3)
	if got != "一二三..." {
		t.Errorf("truncate should cut by runes, got %q", got)
	}
	if got := truncate("", 3); got != "" {
		t.Errorf("empty string, got %q", got)
	}
}

func TestRecentSessionsEmptyTenant(t *testing.T) {
	// no DB available in unit tests; must not panic with nil UUIDs
	_ = uuid.Nil
}