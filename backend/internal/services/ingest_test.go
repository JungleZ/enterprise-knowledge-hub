package services

import (
	"reflect"
	"strings"
	"testing"
)

func TestChunkText_EmptyAndShort(t *testing.T) {
	s := NewIngestService("", nil, nil)

	if got := s.ChunkText("", 0, 0); len(got) != 0 {
		t.Errorf("empty text should produce 0 chunks, got %d", len(got))
	}

	short := "只有一句话。"
	got := s.ChunkText(short, 0, 0)
	if len(got) != 1 || got[0].Text != short {
		t.Errorf("short text should stay as one chunk, got %#v", got)
	}
}

func TestChunkText_OverlapAndMerge(t *testing.T) {
	s := NewIngestService("", nil, nil)

	// 1 very long paragraph (> 2*size) must be force-split with overlap
	longLine := ""
	for i := 0; i < 60; i++ {
		longLine += "这是一段用于测试分块的长文本内容。"
	}
	chunks := s.ChunkText(longLine, 500, 60)
	if len(chunks) < 2 {
		t.Fatalf("long single paragraph should be split, got %d chunks", len(chunks))
	}
	// every chunk respects size limit
	for i, c := range chunks {
		if runes := len([]rune(c.Text)); runes > 1000 {
			t.Errorf("chunk %d exceeds hard limit: %d runes", i, runes)
		}
	}

	// small paragraphs that fit together must be merged into one chunk
	input := "第一段内容。\n第二段内容。\n第三段内容。\n第四段内容。"
	got := s.ChunkText(input, 500, 60)
	if len(got) != 1 {
		t.Fatalf("four short paragraphs should merge into 1 chunk, got %d", len(got))
	}
	for _, want := range []string{"第一段内容。", "第二段内容。", "第三段内容。", "第四段内容。"} {
		if !containsStr(got[0].Text, want) {
			t.Errorf("merged chunk missing %q: %q", want, got[0].Text)
		}
	}
}

func TestChunkText_BlankLinesDropped(t *testing.T) {
	s := NewIngestService("", nil, nil)
	got := s.ChunkText("\n\n\n\n第一行\n\n\n\n", 10, 0)
	if len(got) != 1 || got[0].Text != "第一行" {
		t.Errorf("blank lines should be dropped, got %#v", got)
	}
}

// Consecutive chunks must share a real trailing overlap so boundary sentences
// are not lost on either side.
func TestChunkText_TrueOverlap(t *testing.T) {
	s := NewIngestService("", nil, nil)
	p := strings.Repeat("甲", 40)
	q := strings.Repeat("乙", 40)
	r := strings.Repeat("丙", 40)
	chunks := s.ChunkText(p+"\n"+q+"\n"+r, 50, 10)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	prev := []rune(chunks[0].Text)
	tail := string(prev[len(prev)-10:])
	if !strings.HasPrefix(chunks[1].Text, tail) {
		t.Errorf("chunk 1 should start with overlap tail of chunk 0\nchunk0 tail: %q\nchunk1: %q", tail, chunks[1].Text)
	}
}

// Chinese section headings should be detected and attached to the chunks that
// fall under them, so the embedding context prefix knows the section.
func TestChunkText_HeadingTracked(t *testing.T) {
	s := NewIngestService("", nil, nil)
	text := "一、申报条件\n申请人为天河区注册企业。\n二、申报材料\n需提交营业执照。"
	chunks := s.ChunkText(text, 20, 0)
	found := false
	for _, c := range chunks {
		if containsStr(c.Text, "营业执照") {
			if c.Heading != "二、申报材料" {
				t.Errorf("chunk heading = %q, want %q", c.Heading, "二、申报材料")
			}
			found = true
		}
	}
	if !found {
		t.Errorf("no chunk contains 营业执照: %#v", chunks)
	}
}

func TestVisibilityFromTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty -> public", "", []string{"public"}},
		{"all -> public", "all", []string{"public"}},
		{"全员 -> public", "全员", []string{"public"}},
		{"公开 -> public", "公开", []string{"public"}},
{"dept tags", "财务部,研发部", []string{"财务部", "研发部"}},
	{"mixed all keyword + dept", "研发部,全部", []string{"研发部", "public"}},
	{"whitespace trimmed", " 财务部 , 研发部 ", []string{"财务部", "研发部"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := visibilityFromTags(c.in)
			if !sameSet(got, c.want) {
				t.Errorf("visibilityFromTags(%q) = %v, want set %v", c.in, got, c.want)
			}
		})
	}
}

// sameSet compares two string slices regardless of order.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, x := range a {
		counts[x]++
	}
	for _, x := range b {
		counts[x]--
		if counts[x] < 0 {
			return false
		}
	}
	return true
}

func TestVisibleTagsForUser(t *testing.T) {
	// admins see everything (nil = no visibility filter)
	if got := visibleTagsForUser("财务部", true); got != nil {
		t.Errorf("admin should have nil visibility, got %v", got)
	}
	// members always see public + their own department (sorted)
	if got := visibleTagsForUser("财务部", false); !reflect.DeepEqual(got, []string{"public", "财务部"}) {
		t.Errorf("member tags = %v, want [public 财务部]", got)
	}
	// 全员 department maps to public only (already covered by "public")
	if got := visibleTagsForUser("全员", false); !reflect.DeepEqual(got, []string{"public"}) {
		t.Errorf("全员 member tags = %v, want [public]", got)
	}
}

// containsStr helper for test assertions.
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}