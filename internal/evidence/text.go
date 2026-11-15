package evidence

import (
	"bufio"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// What a run may show the public as text is decided by an allow-list rather
// than by naming what to keep out: a file is publishable only when it is a
// known text type, reads as text, and carries no image or video inside it.
// Everything else stays with the run on the machine. The evidence branch and
// the PR body's embedded evidence both ask IsPublishableText, and this file is
// the one owner of the rule.

// publishableTextExtensions are the file types accepted: logs, command output,
// structured results and notes. A type that is a container (an archive, a
// browser trace), that a browser renders (HTML, SVG), or that is unknown is
// deliberately absent.
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

const (
	// mediaTagExpr matches an HTML element that can show an image or a video.
	// "<image" is listed because HTML parsers read it as "<img".
	mediaTagExpr = `(?i:<(?:img|image|picture|source|video|svg|embed|object|iframe)\b)`
	// inlineMediaExpr matches an image or a video carried inside text: a data
	// URI, or the base64 form of a file header, which is how a report, a
	// capture or a JSON payload holds a screenshot or a recording without
	// naming it: PNG, JPEG (first segment an application marker or a
	// quantization table), GIF, RIFF (WebP, AVI), MP4 (an "ftyp" box at offset
	// 4) and WebM (the EBML magic).
	inlineMediaExpr = `(?i:data:\s*(?:image|video)/)|iVBORw0KGgo|/9j/[24-7]|R0lGOD[lh]h|UklGR|AAAA[A-Za-z0-9+/][GWm2]Z0eX|GkXfo`
)

var (
	// MediaTagPattern matches an HTML element that can show an image or a
	// video.
	MediaTagPattern = regexp.MustCompile(mediaTagExpr)

	inlineMediaPattern = regexp.MustCompile(inlineMediaExpr)
	// markdownMediaPattern is inlineMediaPattern plus what a rendered Markdown
	// file would display: an image embed or a media element.
	markdownMediaPattern = regexp.MustCompile(inlineMediaExpr + `|!\[|` + mediaTagExpr)
)

// HasPublishableTextType reports whether name's extension is one of the text
// types accepted. It reads nothing, so a file of any other type is skipped
// before its size is even looked at.
func HasPublishableTextType(name string) bool {
	return publishableTextExtensions[strings.ToLower(path.Ext(strings.TrimSpace(name)))]
}

// IsPublishableText reports whether the file called name, read from r, may be
// shown to the public: a publishable text type, valid UTF-8 with no NUL byte,
// no image or video carried inside it, and for Markdown no image embed or media
// element. It reads r as a stream and stops at the first reason to refuse, so a
// file of any size is checked whole without being held in memory. A read error
// refuses.
func IsPublishableText(name string, r io.Reader) bool {
	if !HasPublishableTextType(name) {
		return false
	}
	pattern := inlineMediaPattern
	if markdownExtensions[strings.ToLower(path.Ext(strings.TrimSpace(name)))] {
		pattern = markdownMediaPattern
	}
	scan := &textScan{r: bufio.NewReader(r)}
	return !pattern.MatchReader(scan) && !scan.refused
}

// textScan hands runes to the media patterns and ends the scan, refusing the
// file, at a NUL, at a byte that is not UTF-8, or at a read error.
type textScan struct {
	r       *bufio.Reader
	refused bool
}

func (s *textScan) ReadRune() (rune, int, error) {
	r, size, err := s.r.ReadRune()
	if err == nil && (r == 0 || (r == utf8.RuneError && size == 1)) {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		if err != io.EOF {
			s.refused = true
		}
		return 0, 0, io.EOF
	}
	return r, size, nil
}
