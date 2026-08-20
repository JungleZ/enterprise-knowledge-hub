package search

import (
	"reflect"
	"testing"
)

func TestExpandQueries(t *testing.T) {
	qs := expandQueries("请问公司年假制度是怎样的？")
	if len(qs) == 0 {
		t.Fatal("non-empty query should produce candidates")
	}
	// noise words must be stripped: expect a "cleaned" candidate that kept
	// the meaningful keywords without 请问/是/怎样 etc.
	found := false
	for _, q := range qs {
		if q != "请问公司年假制度是怎样的？" && containsAll(q, "年假制度") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a cleaned candidate among %v", qs)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !containsStr(s, sub) {
			return false
		}
	}
	return true
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestExpandQueries_Dedup(t *testing.T) {
	qs := expandQueries("年假 年假")
	seen := map[string]bool{}
	for _, q := range qs {
		if seen[q] {
			t.Errorf("duplicate candidate %q", q)
		}
		seen[q] = true
	}
}

func TestExpandQueries_SingleChar(t *testing.T) {
	// "a" produces no bigrams; should not crash and no empty candidates
	qs := expandQueries("a")
	for _, q := range qs {
		if q == "" {
			t.Errorf("empty candidate not allowed")
		}
	}
}

func TestStripCJKNoise(t *testing.T) {
	// noise words (包括 "请问"、"如何"、"的"、"公司" 等) are stripped;
	// punctuation is left in place (search is tolerant of it).
	cases := []struct {
		in, want string
	}{
		{"请问如何申请报销？", "申请报销？"},
		{"公司的休假制度", "休假制度"},
		{"帮我看看", "看看"},
	}
	for _, c := range cases {
		if got := stripCJKNoise(c.in); got != c.want {
			t.Errorf("stripCJKNoise(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCjkBigrams(t *testing.T) {
	got := cjkBigrams("年假制度")
	want := []string{"年假", "假制", "制度"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cjkBigrams = %v, want %v", got, want)
	}
	// non-CJK chars are skipped: only 年+假 remain -> 1 bigram
	if got := cjkBigrams("AB年C假"); len(got) != 1 || got[0] != "年假" {
		t.Errorf("mixed input should yield 1 bigram, got %v", got)
	}
}

func TestSplitWords(t *testing.T) {
	got := splitWords("如何 使用 API")
	if !reflect.DeepEqual(got, []string{"如何", "使用", "API"}) {
		t.Errorf("splitWords = %v", got)
	}
}

func TestJoinQuoted(t *testing.T) {
	got := joinQuoted([]string{"public", "研发部"})
	want := `"public","研发部"`
	if got != want {
		t.Errorf("joinQuoted = %s, want %s", got, want)
	}
}

func TestIsIndexExistsError(t *testing.T) {
	if !isIndexExistsError(errStr("index already exists")) {
		t.Error("should recognize 'index already exists'")
	}
	if !isIndexExistsError(errStr("index_already_exists: foo")) {
		t.Error("should recognize index_already_exists")
	}
	if isIndexExistsError(errStr("boom")) {
		t.Error("unrelated error should not match")
	}
	if isIndexExistsError(nil) {
		t.Error("nil error should not match")
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }