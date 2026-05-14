package msg

import "testing"

// dawn 2026-05-14 新增敏感词过滤：验证中文词按字符数等长替换。
func TestMaskBySensitiveWords(t *testing.T) {
	got, changed := maskBySensitiveWords("你好敏感词和坏词", []string{"敏感词", "坏词"})
	if !changed {
		t.Fatal("expected content to be masked")
	}
	want := "你好***和**"
	if got != want {
		t.Fatalf("masked content mismatch, got %q want %q", got, want)
	}
}
