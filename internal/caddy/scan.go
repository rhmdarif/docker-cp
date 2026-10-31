package caddy

import (
	"errors"
	"strings"
	"unicode"
)

var errUnbalanced = errors.New("kurung kurawal tidak seimbang")

type token struct {
	text   string
	quoted bool
	// depth is the brace depth before this token; for unquoted tokens it is
	// computed per character, so "}" has depth 1 and brings the level to 0.
	depth int
	// endDepth is the depth after the token.
	endDepth int
	line     int
}

// scan tokenizes s the way Caddy's Caddyfile lexer does for the purposes of
// brace tracking: quotes (" and `) only open a string at the start of a
// token, `#` only starts a comment at the start of a token, backslash escapes
// inside "..." only. Unquoted braces are counted per character, which is
// stricter than Caddy (Caddy only treats a lone "{" / "}" as structure), so
// anything this scanner accepts as balanced is balanced for Caddy too.
// Heredocs and ambiguous quoting are rejected outright.
func scan(s string) ([]token, error) {
	var toks []token
	rs := []rune(s)
	depth, line := 0, 1
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case c == '\n':
			line++
			i++
		case unicode.IsSpace(c):
			i++
		case c == '#':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case c == '"' || c == '`':
			start := line
			var sb strings.Builder
			i++
			closed := false
			for i < len(rs) {
				ch := rs[i]
				if ch == '\n' {
					line++
				}
				if c == '"' && ch == '\\' && i+1 < len(rs) {
					if rs[i+1] == '"' {
						sb.WriteRune('"')
					} else {
						sb.WriteRune('\\')
						sb.WriteRune(rs[i+1])
					}
					if rs[i+1] == '\n' {
						line++
					}
					i += 2
					continue
				}
				if ch == c {
					closed = true
					i++
					break
				}
				sb.WriteRune(ch)
				i++
			}
			if !closed {
				return nil, errors.New("tanda kutip tidak ditutup")
			}
			if i < len(rs) && !unicode.IsSpace(rs[i]) {
				return nil, errors.New("tanda kutip harus diikuti spasi")
			}
			toks = append(toks, token{text: sb.String(), quoted: true, depth: depth, endDepth: depth, line: start})
		default:
			start := i
			d := depth
			for i < len(rs) && !unicode.IsSpace(rs[i]) {
				switch rs[i] {
				case '{':
					d++
				case '}':
					d--
					if d < 0 {
						return nil, errUnbalanced
					}
				case '<':
					if i+1 < len(rs) && rs[i+1] == '<' {
						return nil, errors.New("heredoc (<<) tidak diizinkan")
					}
				}
				i++
			}
			toks = append(toks, token{text: string(rs[start:i]), depth: depth, endDepth: d, line: line})
			depth = d
		}
	}
	if depth != 0 {
		return nil, errUnbalanced
	}
	return toks, nil
}
