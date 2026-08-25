package spi

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// patternMatcher answers "does this stored value match the leaf's operand".
// LIKE and MATCHES_PATTERN both produce one; nothing else does.
type patternMatcher interface{ matches(s string) bool }

// LIKE is a glob, not a regex. The grammar, which is PostgreSQL's and
// SQLite's `LIKE ... ESCAPE '\'`:
//
//   - '%'  any sequence of characters, including empty, INCLUDING newlines
//   - '_'  exactly one character (one rune), INCLUDING a newline
//   - '\X' the literal character X, for ANY X — so \%, \_ and \\ are literal
//     '%', '_' and '\', and \d is a literal 'd'
//   - anything else, itself
//
// The match is whole-string and case-sensitive. A trailing unpaired '\' is the
// only malformed pattern; every other operand matches something.
//
// This is deliberately NOT Cloud's Like.prepareSpecialCharacters, which
// translates to a regex and leaks RE2 escapes. cyoda-go leads this contract.
type likeTokKind uint8

const (
	tokLit likeTokKind = iota // literal text, matched bytewise
	tokOne                    // '_'
	tokAny                    // '%'
)

type likeTok struct {
	kind likeTokKind
	lit  string // tokLit only
}

// likePattern is immutable after construction: matches takes no lock and
// writes no field, because a prepared plan tree is evaluated concurrently.
type likePattern struct{ toks []likeTok }

// parseLikePattern tokenises operand. It allocates once, at Prepare time;
// matches allocates nothing.
func parseLikePattern(operand string) (patternMatcher, error) {
	var (
		toks []likeTok
		lit  strings.Builder
	)
	flush := func() {
		if lit.Len() > 0 {
			toks = append(toks, likeTok{kind: tokLit, lit: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(operand); {
		switch operand[i] {
		case '\\':
			if i+1 >= len(operand) {
				// Byte offset, not the operand: this error reaches a 400.
				return nil, fmt.Errorf("%w: LIKE pattern ends with an unpaired escape at byte %d",
					ErrInvalidPattern, i)
			}
			r, sz := utf8.DecodeRuneInString(operand[i+1:])
			lit.WriteRune(r)
			i += 1 + sz
		case '%':
			flush()
			// Collapse runs of '%': "%%%%" behaves as "%" and cannot make the
			// scan below quadratic in the number of stars.
			if n := len(toks); n == 0 || toks[n-1].kind != tokAny {
				toks = append(toks, likeTok{kind: tokAny})
			}
			i++
		case '_':
			flush()
			toks = append(toks, likeTok{kind: tokOne})
			i++
		default:
			r, sz := utf8.DecodeRuneInString(operand[i:])
			lit.WriteRune(r)
			i += sz
		}
	}
	flush()
	return &likePattern{toks: toks}, nil
}

// matches runs the standard greedy wildcard scan with a single backtrack
// point at the most recent '%'. Linear in len(s) per star, never exponential.
func (p *likePattern) matches(s string) bool {
	starTok, starPos := -1, 0
	i, j := 0, 0
	for j < len(s) {
		if i < len(p.toks) {
			switch t := p.toks[i]; t.kind {
			case tokLit:
				if strings.HasPrefix(s[j:], t.lit) {
					j += len(t.lit)
					i++
					continue
				}
			case tokOne:
				_, sz := utf8.DecodeRuneInString(s[j:])
				j += sz
				i++
				continue
			case tokAny:
				starTok, starPos = i, j
				i++
				continue
			}
		}
		if starTok < 0 {
			return false
		}
		// Give the star one more character and retry from just after it.
		_, sz := utf8.DecodeRuneInString(s[starPos:])
		if sz == 0 {
			return false
		}
		starPos += sz
		i, j = starTok+1, starPos
	}
	for i < len(p.toks) && p.toks[i].kind == tokAny {
		i++
	}
	return i == len(p.toks)
}
