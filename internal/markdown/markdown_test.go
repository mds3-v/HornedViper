package markdown

import (
	"strings"
	"testing"
)

func TestRenderMarkdownAndSanitize(t *testing.T) {
	out := string(Render("**bold** and `code`\n\n```go\nfmt.Println(1)\n```"))
	if !strings.Contains(out, "<strong>bold</strong>") || !strings.Contains(out, "<code>code</code>") {
		t.Fatalf("markdown not rendered: %s", out)
	}
	bad := string(Render(`<script>alert(1)</script> [x](javascript:alert(1))`))
	if strings.Contains(bad, "<script") || strings.Contains(bad, "javascript:") {
		t.Fatalf("unsafe html survived: %s", bad)
	}
}
