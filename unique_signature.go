package spi

// Signature grammar for ComputeClaims
//
// Each field value is encoded as a type-tagged token; all tokens for a key are
// joined by the ASCII Unit Separator byte (0x1F, \x1f) to form the signature.
//
//	string  →  s{len}:{value}     (length-prefix is mandatory — see below)
//	bool    →  b:true | b:false
//	number  →  n:{reduced-rational}   (via math/big.Rat.RatString; 42, 42.0, 4.2e1 → "n:42")
//
// The string length-prefix (`s{len}:`) is what guarantees INJECTIVITY (collision
// safety): without it, a value containing the separator byte or mimicking an
// adjacent token's prefix (e.g. "s2:ab") could forge another value-set's
// signature. The length-prefix MUST NOT be removed or simplified.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// maxCoeffDigits is the maximum number of coefficient characters (integer digits
// plus fractional digits, including any leading fractional zeros) in a numeric
// literal that ComputeClaims will materialize via math/big. This is a
// conservative DoS pre-filter: literals whose coefficient exceeds this bound are
// rejected as ErrPartialUniqueKey without any big-number allocation.
//
// Note: this counts ALL coefficient characters, not true significant digits —
// e.g. "0.000…1" with more than 64 coefficient chars is rejected while "1e-66"
// (one coefficient char) passes. The filter fails closed: pathological
// leading-zero-padded representations produce a 4xx ErrPartialUniqueKey.
const maxCoeffDigits = 64

// maxNumExp is the maximum absolute exponent magnitude accepted.
// 6144 matches IEEE 754 decimal128 range; any real-world value fits within it.
const maxNumExp = 6144

// ComputeClaims derives a UniqueClaim for each UniqueKey where every declared
// field is present and non-null in the document. Rules:
//   - All fields absent/null → no claim (the key is not applicable to this doc).
//   - Some-but-not-all present/non-null → error wrapping ErrPartialUniqueKey.
//   - Non-scalar value at a declared path → error wrapping ErrPartialUniqueKey.
//   - Over-bound numeric literal → error wrapping ErrPartialUniqueKey.
//
// Signatures are opaque type-tagged canonical strings; numeric values are
// canonicalized so 42, 42.0, and 4.2e1 produce identical signatures.
func ComputeClaims(keys []UniqueKey, doc []byte) ([]UniqueClaim, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	// Decode with UseNumber so json.Number is preserved as a string token
	// (not as float64, which would lose precision for large integers).
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	// Callers guarantee a schema-validated JSON object document; reaching this
	// branch is an internal invariant violation, not a user input error.
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("compute claims: expected JSON object document: %w", err)
	}

	var claims []UniqueClaim
	for _, key := range keys {
		claim, err := computeOneClaim(key, root)
		if err != nil {
			return nil, err
		}
		if claim != nil {
			claims = append(claims, *claim)
		}
	}
	return claims, nil
}

// computeOneClaim derives a single UniqueClaim (or nil if all-absent/all-null).
func computeOneClaim(key UniqueKey, root map[string]any) (*UniqueClaim, error) {
	tokens := make([]string, 0, len(key.Fields))
	presentCount := 0

	for _, field := range key.Fields {
		val, found, err := walkPath(field, root)
		if err != nil {
			return nil, err
		}
		if !found || val == nil {
			// absent or null — counts as not present
			continue
		}
		tok, err := tokenize(field, val)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
		presentCount++
	}

	if presentCount == 0 {
		// All absent or null — no claim, not an error.
		return nil, nil
	}
	if presentCount != len(key.Fields) {
		return nil, fmt.Errorf("%w: key %q has only %d of %d fields present",
			ErrPartialUniqueKey, key.ID, presentCount, len(key.Fields))
	}

	sig := strings.Join(tokens, "\x1f")
	return &UniqueClaim{KeyID: key.ID, Signature: sig}, nil
}

// walkPath resolves a dotted JSONPath (e.g. "$.a.b") by splitting on "."
// and walking nested maps. Returns (value, true, nil) if found, (nil, false, nil)
// if absent, or an error if an intermediate segment is not a map.
//
// Supported grammar: dot-separated object-key traversal to a scalar leaf.
// Limitations key authors must be aware of:
//   - No array indexing: "$.items[0]" is silently treated as a missing map key,
//     not an element lookup.
//   - Literal dots in key names are not addressable: a JSON key "a.b" cannot be
//     reached — the path "$.a.b" always descends into nested objects.
func walkPath(field string, root map[string]any) (any, bool, error) {
	// Drop the leading "$" — paths look like "$.foo" or "$.a.b".
	path := strings.TrimPrefix(field, "$")
	if path == "" {
		// "$" alone refers to the root — treated as absent for scalar extraction.
		return nil, false, nil
	}
	// Split on "." — first segment is empty if path starts with ".".
	parts := strings.Split(path, ".")
	// parts[0] is "" (from the leading "."), so we skip it.
	var current any = root
	for _, seg := range parts {
		if seg == "" {
			continue
		}
		m, ok := current.(map[string]any)
		if !ok {
			// Intermediate node is not a map — the path doesn't exist in this doc.
			return nil, false, nil
		}
		val, exists := m[seg]
		if !exists {
			return nil, false, nil
		}
		current = val
	}
	return current, true, nil
}

// tokenize converts a resolved JSON value to its type-tagged canonical token.
// Objects and arrays are rejected as non-scalar.
func tokenize(field string, val any) (string, error) {
	switch v := val.(type) {
	case string:
		return fmt.Sprintf("s%d:%s", len(v), v), nil
	case bool:
		if v {
			return "b:true", nil
		}
		return "b:false", nil
	case json.Number:
		return canonNum(v)
	case map[string]any, []any:
		return "", fmt.Errorf("%w: non-scalar value at field %q", ErrPartialUniqueKey, field)
	default:
		// Unexpected type — treat as non-scalar.
		return "", fmt.Errorf("%w: unsupported value type at field %q", ErrPartialUniqueKey, field)
	}
}

// canonNum canonicalizes a JSON numeric literal to a stable string.
// It first checks the literal's digit count and exponent magnitude against
// maxCoeffDigits / maxNumExp WITHOUT materializing any big.Int, then delegates
// to math/big.Rat for normalization. 42, 42.0, and 4.2e1 all produce "n:42".
func canonNum(n json.Number) (string, error) {
	s := string(n)
	digits, exp, err := countDigitsExp(s)
	if err != nil {
		return "", fmt.Errorf("%w: malformed numeric literal %q: %v", ErrPartialUniqueKey, s, err)
	}
	if digits > maxCoeffDigits || exp > maxNumExp || exp < -maxNumExp {
		return "", fmt.Errorf("%w: numeric literal out of bounds %q (coeffDigits=%d exp=%d)",
			ErrPartialUniqueKey, s, digits, exp)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", fmt.Errorf("%w: uncanonicalizable number %q", ErrPartialUniqueKey, s)
	}
	return "n:" + r.RatString(), nil
}

// countDigitsExp parses the coefficient-character count and effective exponent
// from a JSON numeric literal string WITHOUT materializing any big value.
//
// For "4.2e1": coefficient "4.2" has 2 coefficient chars; fractional digits = 1;
// explicit exponent = 1; effective exponent = explicit - fractional = 0.
// For "1e1000000000": coeffDigits = 1, exp = 1_000_000_000.
//
// Returns (coeffDigits, effectiveExponent, error).
func countDigitsExp(s string) (int, int, error) {
	if s == "" {
		return 0, 0, fmt.Errorf("empty string")
	}

	i := 0
	// Optional leading sign.
	if s[i] == '-' || s[i] == '+' {
		i++
	}
	if i >= len(s) {
		return 0, 0, fmt.Errorf("sign only")
	}

	// Count integer digits.
	intDigits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		intDigits++
		i++
	}

	// Optional fractional part.
	fracDigits := 0
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			fracDigits++
			i++
		}
	}

	// Optional exponent.
	explicitExp := 0
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		expSign := 1
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			if s[i] == '-' {
				expSign = -1
			}
			i++
		}
		if i >= len(s) {
			return 0, 0, fmt.Errorf("exponent with no digits")
		}
		expVal := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			// Detect overflow early: if expVal would exceed maxNumExp+1, cap it
			// (we only need to know it's too large, not the exact value).
			if expVal <= maxNumExp+1 {
				expVal = expVal*10 + int(s[i]-'0')
			}
			i++
		}
		explicitExp = expSign * expVal
	}

	if i != len(s) {
		return 0, 0, fmt.Errorf("unexpected character at position %d", i)
	}

	coeffDigits := intDigits + fracDigits
	effectiveExp := explicitExp - fracDigits
	return coeffDigits, effectiveExp, nil
}
