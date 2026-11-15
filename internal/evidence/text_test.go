package evidence

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"
)

func TestIsPublishableText(t *testing.T) {
	t.Parallel()
	large := strings.Repeat("request served in 12ms\n", 20000)
	for _, tc := range []struct {
		name    string
		file    string
		content string
		want    bool
	}{
		{"plain log", "server.log", "POST /checkout 200\n", true},
		{"upper-case extension", "RESULTS.JSON", `{"passed": 3}`, true},
		{"empty text file", "empty.txt", "", true},
		{"non-ASCII text", "notes.txt", "café ✓\n", true},
		{"plain Markdown", "notes.md", "# Checkout\n\nAll [steps](steps.md) passed.\n", true},
		{"large log", "server.log", large, true},
		{"no extension", "output", "POST /checkout 200\n", false},
		{"archive", "trace.zip", "PK archive that happens to read as text", false},
		{"HTML report", "report.html", "<html><body>1 passed</body></html>", false},
		{"SVG", "chart.svg", "<svg></svg>", false},
		{"NUL byte", "capture.log", "abc\x00def", false},
		{"not UTF-8", "capture.txt", "abc\xff\xfedef", false},
		{"data URI", "payload.json", `{"shot":"data:image/png;base64,AAAA"}`, false},
		{"video data URI", "payload.json", `{"clip":"DATA:video/mp4;base64,AAAA"}`, false},
		{"base64 PNG header", "capture.json", `{"body":"iVBORw0KGgoAAAANSUhEUg"}`, false},
		{"base64 JPEG header", "capture.txt", "body: /9j/4AAQSkZJRgABAQ", false},
		{"base64 GIF header", "capture.txt", "body: R0lGODlhAQABAIAAAP", false},
		{"base64 WebP header", "capture.txt", "body: UklGRiQAAABXRUJQVlA4", false},
		{"image late in a large file", "server.log", large + "data:image/png;base64,AAAA\n" + large, false},
		{"Markdown image embed", "notes.md", "# Checkout\n\n![page](checkout.png)\n", false},
		{"Markdown media element", "notes.markdown", "<IMG src=checkout.png>", false},
		{"image embed syntax in a log is text", "server.log", "rendered ![page](checkout.png)\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsPublishableText(tc.file, strings.NewReader(tc.content)); got != tc.want {
				t.Fatalf("IsPublishableText(%q) = %v, want %v", tc.file, got, tc.want)
			}
		})
	}
}

func TestIsPublishableText_ReadErrorRefuses(t *testing.T) {
	t.Parallel()
	r := iotest.ErrReader(errors.New("disk gone"))
	if IsPublishableText("server.log", r) {
		t.Fatal("a file that cannot be read whole must not be published")
	}
}
