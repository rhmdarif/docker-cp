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
	// depth is the brace depth before this token.
	depth int
	// endDepth is the depth after the token.
	endDepth int
	line     int
}

// scan tokenizes s the way Caddy's Caddyfile lexer does for the purposes of
// brace tracking: quotes (" and `) only open a string at the start of a
// token, `#` only starts a comment at the start of a token, backslash escapes
// inside "..." only. Only a token that is exactly "{" or "}" changes the
// depth; braces inside longer tokens (a{, {remote_host}) are literal, as in
// Caddy. Heredocs, ambiguous quoting and unquoted tokens starting with a
// backslash (Caddy still opens strings/comments after one) are rejected.
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
			for i < len(rs) && !unicode.IsSpace(rs[i]) {
				if rs[i] == '<' && i+1 < len(rs) && rs[i+1] == '<' {
					return nil, errors.New("heredoc (<<) tidak diizinkan")
				}
				i++
			}
			text := string(rs[start:i])
			if text[0] == '\\' {
				return nil, errors.New("token tidak boleh diawali backslash")
			}
			d := depth
			switch text {
			case "{":
				d++
			case "}":
				d--
				if d < 0 {
					return nil, errUnbalanced
				}
			}
			toks = append(toks, token{text: text, depth: depth, endDepth: d, line: line})
			depth = d
		}
	}
	if depth != 0 {
		return nil, errUnbalanced
	}
	return toks, nil
}

// hasImport reports whether any token is the import directive.
func hasImport(toks []token) bool {
	for _, t := range toks {
		if t.text == "import" {
			return true
		}
	}
	return false
}
