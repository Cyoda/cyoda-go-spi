package spi

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// patternMatcher answers "does this stored value match the leaf's operand".
// LIKE and MATCHES_PATTERN both produce one; nothing else does.
type patternMatcher interface{ matches(s string) bool }

// likeTokKind classifies one token of a tokenised LIKE pattern: literal text,
// a '_' single-char wildcard, or a '%' any-sequence wildcard. See [FilterLike]
// for the grammar this tokenises.
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

// parseLikePattern is parseLikePatternImpl behind a package var, mirroring
// [compileRegex]: an internal test counts calls to prove tokenisation happens
// once per query (at Prepare time) rather than once per row. Production code
// never reassigns it.
var parseLikePattern = parseLikePatternImpl

// parseLikePatternImpl tokenises operand. It allocates once, at Prepare time;
// matches allocates nothing.
func parseLikePatternImpl(operand string) (patternMatcher, error) {
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
				return nil, fmt.Errorf("%w: pattern ends with an unpaired escape at byte %d",
					ErrInvalidPattern, i)
			}
			// WriteString, not decode-then-WriteRune: a raw invalid UTF-8
			// byte must stay byte-identical, not get transcoded to U+FFFD
			// (see the default branch below for why).
			_, sz := utf8.DecodeRuneInString(operand[i+1:])
			lit.WriteString(operand[i+1 : i+1+sz])
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
			// WriteString, not decode-then-WriteRune: an invalid UTF-8 byte
			// decodes to (utf8.RuneError, 1); WriteRune would re-encode that
			// as the three bytes of U+FFFD, silently transcoding the
			// operand. Literals are compared bytewise, so that would make
			// LIKE "\xff" fail against the byte-identical "\xff" while
			// wrongly matching "�" — inconsistent with EQ/CONTAINS on
			// the same pair. WriteString keeps the original byte(s).
			_, sz := utf8.DecodeRuneInString(operand[i:])
			lit.WriteString(operand[i : i+sz])
			i += sz
		}
	}
	flush()
	return &likePattern{toks: toks}, nil
}

// matches runs the standard greedy wildcard scan with a single backtrack
// point at the most recent '%'. strings.HasPrefix re-scans the current
// literal at each backtrack step, so this is O(len(s) × len(literal)) per
// star, never exponential.
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
		// starPos is always < len(s) here: it is only ever set to a j from
		// the outer `for j < len(s)` guard, and an increment that reaches
		// len(s) makes i,j = starTok+1, starPos fail that guard and exit the
		// loop before this line runs again — so sz is never 0.
		_, sz := utf8.DecodeRuneInString(s[starPos:])
		starPos += sz
		i, j = starTok+1, starPos
	}
	for i < len(p.toks) && p.toks[i].kind == tokAny {
		i++
	}
	return i == len(p.toks)
}
