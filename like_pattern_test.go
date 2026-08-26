package spi

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func mustLike(t *testing.T, pattern string) patternMatcher {
	t.Helper()
	m, err := parseLikePattern(pattern)
	if err != nil {
		t.Fatalf("parseLikePattern(%q) unexpected error: %v", pattern, err)
	}
	return m
}

func TestParseLikePattern_Grammar(t *testing.T) {
	cases := []struct {
		pattern, in string
		want        bool
	}{
		// %: any run, including empty and newlines
		{"%", "", true},
		{"%", "anything", true},
		{"%", "a\nb", true},
		{"foo%", "foobar", true},
		{"foo%", "foo", true},
		{"foo%", "xfoo", false},
		{"%foo%", "xxfooyy", true},
		// _: exactly one rune, including a newline
		{"a_c", "abc", true},
		{"a_c", "ac", false},
		{"a_b", "a\nb", true},
		{"_", "é", true},
		{"_", "ab", false},
		// \X is literal X, for any X — the SQL rule
		{`\d`, "d", true},
		{`\d`, "7", false},
		{`\d`, `\d`, false},
		{`\%`, "%", true},
		{`\%`, "5000", false},
		{`\_`, "_", true},
		{`\_`, "x", false},
		{`\\`, `\`, true},
		{`a\\b`, `a\b`, true},
		{`\[`, "[", true},
		// everything else is a literal, including regex metacharacters
		{"1.2", "1.2", true},
		{"1.2", "1x2", false},
		{"[a]", "[a]", true},
		{"a<b>c", "a<b>c", true},
		// whole-string anchored, case-sensitive
		{"abc", "xabcx", false},
		{"abc", "ABC", false},
		// empty pattern matches only the empty string
		{"", "", true},
		{"", "a", false},
	}
	for _, c := range cases {
		if got := mustLike(t, c.pattern).matches(c.in); got != c.want {
			t.Errorf("LIKE %q vs %q = %v, want %v", c.pattern, c.in, got, c.want)
		}
	}
}

func TestParseLikePattern_TrailingEscapeRejected(t *testing.T) {
	for _, pattern := range []string{`\`, `a\`, `%\`, `\\\`} {
		m, err := parseLikePattern(pattern)
		if err == nil {
			t.Errorf("parseLikePattern(%q) = nil error, want rejection", pattern)
			continue
		}
		if !errors.Is(err, ErrInvalidPattern) {
			t.Errorf("parseLikePattern(%q) error %v does not wrap ErrInvalidPattern", pattern, err)
		}
		if m != nil {
			t.Errorf("parseLikePattern(%q) returned a non-nil matcher alongside an error", pattern)
		}
		if strings.Contains(err.Error(), pattern) {
			t.Errorf("parseLikePattern(%q) error echoes the operand: %v", pattern, err)
		}
	}
	// An EVEN number of trailing backslashes is a literal, not an unpaired escape.
	if !mustLike(t, `a\\`).matches(`a\`) {
		t.Error(`LIKE "a\\\\" should match "a\\"`)
	}
}

func TestParseLikePattern_InvalidUTF8(t *testing.T) {
	// One invalid byte decodes as one U+FFFD, so "_" consumes exactly one.
	if !mustLike(t, "_").matches("\xff") {
		t.Error(`LIKE "_" should match a single invalid byte`)
	}
	if mustLike(t, "_").matches("\xff\xfe") {
		t.Error(`LIKE "_" should not match two invalid bytes`)
	}
}

func TestParseLikePattern_InvalidUTF8InOperand(t *testing.T) {
	// An invalid byte in the OPERAND (the pattern itself, not the subject)
	// must be preserved byte-identical, not transcoded to U+FFFD — literals
	// are compared bytewise, so a decode-then-WriteRune round trip would make
	// this fail against the byte-identical subject and wrongly match "�".
	if !mustLike(t, "\xff").matches("\xff") {
		t.Error(`LIKE "\xff" should match the byte-identical subject "\xff"`)
	}
	if mustLike(t, "\xff").matches("�") {
		t.Error(`LIKE "\xff" should NOT match "�" (U+FFFD)`)
	}
	// Same guarantee through the escape branch: \X is the literal byte X,
	// even when X is an invalid UTF-8 byte.
	if !mustLike(t, "\\\xff").matches("\xff") {
		t.Error(`LIKE "\` + "\\xff" + `" should match the byte-identical subject "\xff"`)
	}
}

func TestParseLikePattern_NoPathologicalBacktracking(t *testing.T) {
	// Would be exponential under a naive recursive matcher. Must finish fast.
	m := mustLike(t, strings.Repeat("%_", 40)+"z")
	if m.matches(strings.Repeat("a", 4000)) {
		t.Error("expected no match")
	}
}

func TestLikePattern_ConcurrentMatchIsRaceFree(t *testing.T) {
	m := mustLike(t, "%foo_bar%")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = m.matches("xxfooZbaryy")
			}
		}()
	}
	wg.Wait()
}
