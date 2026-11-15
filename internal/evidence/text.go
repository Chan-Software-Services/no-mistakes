package evidence

import (
	"bytes"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The evidence branch is public and permanent, so what reaches it is decided by
// an allow-list rather than by naming what to keep out: a file is published
// only when it is a known text type, reads as text, and carries no image or
// video inside it. Everything else stays with the run on the machine. This file
// is the one owner of that rule.

// publishableTextExtensions are the file types the evidence branch accepts:
// logs, command output, structured results and notes. A type that is a
// container (an archive, a browser trace), that a browser renders (HTML, SVG),
// or that is unknown is deliberately absent.
var publishableTextExtensions = map[string]bool{
	".txt": true, ".log": true, ".out": true, ".err": true,
	".md": true, ".markdown": true,
	".json": true, ".jsonl": true, ".ndjson": true,
	".yaml": true, ".yml": true, ".toml": true,
	".csv": true, ".tsv": true, ".xml": true,
	".diff": true, ".patch": true, ".tap": true,
}

// markdownExtensions are the publishable types a forge renders rather than
// shows as source, so an embed inside one would display.
var markdownExtensions = map[string]bool{".md": true, ".markdown": true}

var (
	// MediaTagPattern matches an HTML element that can show an image or a
	// video. "<image" is listed because HTML parsers read it as "<img".
	MediaTagPattern = regexp.MustCompile(`(?i)<(img|image|picture|source|video|svg|embed|object|iframe)\b`)

	// inlineMediaPattern matches an image or a video carried inside text: a
	// data URI, or the base64 form of a PNG, JPEG, GIF or RIFF (WebP, AVI)
	// file header, which is how a report, a HAR capture or a JSON payload holds
	// a screenshot without naming it.
	inlineMediaPattern = regexp.MustCompile(`(?i:data:\s*(?:image|video)/)|iVBORw0KGgo|/9j/[4-7]|R0lGOD[lh]h|UklGR`)
)

// HasPublishableTextType reports whether name's extension is one of the text
// types the evidence branch accepts. It reads nothing, so a file of any other
// type is skipped before its size is even looked at.
func HasPublishableTextType(name string) bool {
	return publishableTextExtensions[strings.ToLower(path.Ext(strings.TrimSpace(name)))]
}

// HasInlineMedia reports whether the text read from r carries an image or a
// video inside it (inlineMediaPattern). It reads r as a stream, so a caller can
// check a file of any size without holding it.
func HasInlineMedia(r io.RuneReader) bool {
	return inlineMediaPattern.MatchReader(r)
}

// IsPublishableText reports whether the file called name holding data may be
// published: a publishable text type, valid UTF-8 with no NUL byte, no image or
// video carried inside it, and for Markdown no image embed or media element.
func IsPublishableText(name string, data []byte) bool {
	if !HasPublishableTextType(name) {
		return false
	}
	if bytes.IndexByte(data, 0) != -1 || !utf8.Valid(data) {
		return false
	}
	if inlineMediaPattern.Match(data) {
		return false
	}
	if markdownExtensions[strings.ToLower(path.Ext(strings.TrimSpace(name)))] {
		if bytes.Contains(data, []byte("![")) || MediaTagPattern.Match(data) {
			return false
		}
	}
	return true
}
