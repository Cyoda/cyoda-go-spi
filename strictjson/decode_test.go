package strictjson_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cyoda-platform/cyoda-go-spi/strictjson"
)

// TestDecode_RefusesInexactMemberNames pins the property every typed request
// body relies on: a member name must spell a field exactly, once.
// encoding/json (v1) matched names ignoring case (and Unicode folding, so
// "tranſitions" with U+017F was read as "transitions") and let a later
// duplicate overwrite or merge into an earlier one.
func TestDecode_RefusesInexactMemberNames(t *testing.T) {
	type transition struct {
		Manual bool `json:"manual"`
	}
	type body struct {
		Transitions []transition `json:"transitions"`
	}
	cases := []struct {
		name, in, want string
	}{
		{"case variant", `{"transitions":[{"Manual":true}]}`, `invalid JSON at "/transitions/0": unknown member name "Manual"`},
		{"unicode fold variant", `{"tran` + "\u017F" + `itions":[]}`, `invalid JSON at "": unknown member name "tranſitions"`},
		{"duplicate", `{"transitions":[{"manual":true,"manual":false}]}`, `invalid JSON at "/transitions/0": duplicate member name "manual"`},
		{"case-variant duplicate", `{"transitions":[],"TRANSITIONS":[]}`, `invalid JSON at "": unknown member name "TRANSITIONS"`},
		{"unknown", `{"transitions":[],"extra":1}`, `invalid JSON at "": unknown member name "extra"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b body
			err := strictjson.Decode([]byte(tc.in), &b)
			if err == nil {
				t.Fatalf("accepted %s as %+v", tc.in, b)
			}
			if err.Error() != tc.want {
				t.Errorf("error\n got: %s\nwant: %s", err, tc.want)
			}
		})
	}
}

// TestDecode_ErrorNamesNoGoTypes: the error text goes into a 400 detail, so
// it names the JSON location and never a Go type or package.
func TestDecode_ErrorNamesNoGoTypes(t *testing.T) {
	type body struct {
		N int `json:"n"`
	}
	for in, want := range map[string]string{
		`{"n":"x"}`:                  `invalid JSON at "/n": a JSON string where an integer is expected`,
		`{"n":1.5}`:                  `invalid JSON at "/n": a JSON number where an integer is expected`,
		`{"n":99999999999999999999}`: `invalid JSON at "/n": a JSON number out of range`,
		`{"n":1e3}`:                  `invalid JSON at "/n": a JSON number where an integer is expected`,
		`{"n":[1]}`:                  `invalid JSON at "/n": a JSON array where an integer is expected`,
		`{"n":1} x`:                  `invalid JSON at offset 8: invalid character 'x' after top-level value`,
		`{"n":1}}`:                   `invalid JSON at offset 7: invalid character '}' after top-level value`,
		`{"n":1} ]`:                  `invalid JSON at offset 8: invalid character ']' after top-level value`,
		`{"n":1}{}`:                  `invalid JSON at offset 7: invalid character '{' after top-level value`,
		`{"n":}`:                     `invalid JSON at offset 5: invalid character '}' at start of value`,
		``:                           `invalid JSON at offset 0: unexpected EOF`,
	} {
		var b body
		err := strictjson.Decode([]byte(in), &b)
		if err == nil {
			t.Fatalf("accepted %q", in)
		}
		if err.Error() != want {
			t.Errorf("%q:\n got: %s\nwant: %s", in, err, want)
		}
	}
}

func TestDecode_Accepts(t *testing.T) {
	type body struct {
		Manual *bool `json:"manual"`
	}
	var b body
	if err := strictjson.Decode([]byte(` {"manual":true} `), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b.Manual == nil || !*b.Manual {
		t.Errorf("got %+v", b)
	}
}

// TestDecode_PreserveNumbers: with PreserveNumbers a JSON number held in an
// `any` — at any depth — keeps its literal as a json.Number, so an integer
// above 2^53 survives (float64 would round it).
func TestDecode_PreserveNumbers(t *testing.T) {
	type body struct {
		Params map[string]any `json:"params"`
		Any    any            `json:"any"`
	}
	var b body
	in := `{"params":{"big":9007199254740993,"list":[1.50,{"n":-0}]},"any":2e3}`
	if err := strictjson.Decode([]byte(in), &b, strictjson.PreserveNumbers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := b.Params["big"]; got != json.Number("9007199254740993") {
		t.Errorf("big: got %#v", got)
	}
	list := b.Params["list"].([]any)
	if list[0] != json.Number("1.50") {
		t.Errorf("list[0]: got %#v", list[0])
	}
	if got := list[1].(map[string]any)["n"]; got != json.Number("-0") {
		t.Errorf("list[1].n: got %#v", got)
	}
	if b.Any != json.Number("2e3") {
		t.Errorf("any: got %#v", b.Any)
	}
}

// TestDecode_AllowUnknownMembers: an open shape (a probe that reads a few
// members, or a JWK, which RFC 7517 lets carry members of its own) ignores
// members it does not name, but a name still matches exactly and a
// duplicate is still refused.
func TestDecode_AllowUnknownMembers(t *testing.T) {
	type probe struct {
		Kty string `json:"kty"`
	}
	var p probe
	if err := strictjson.Decode([]byte(`{"kty":"RSA","use":"sig"}`), &p, strictjson.AllowUnknownMembers); err != nil || p.Kty != "RSA" {
		t.Fatalf("got %+v, %v", p, err)
	}
	p = probe{}
	if err := strictjson.Decode([]byte(`{"KTY":"RSA"}`), &p, strictjson.AllowUnknownMembers); err != nil || p.Kty != "" {
		t.Errorf("a case variant must not fill kty: got %+v, %v", p, err)
	}
	if err := strictjson.Decode([]byte(`{"kty":"RSA","kty":"EC"}`), &p, strictjson.AllowUnknownMembers); err == nil {
		t.Error("accepted a duplicate member name")
	}
}

// selfDecoding decodes itself through Decode, as a generated event type does
// from its UnmarshalJSON method.
type selfDecoding struct {
	Name string `json:"name"`
}

func (n *selfDecoding) UnmarshalJSON(b []byte) error {
	type plain selfDecoding
	var p plain
	if err := strictjson.Decode(b, &p); err != nil {
		return err
	}
	*n = selfDecoding(p)
	return nil
}

type alwaysRequired struct{}

func (*alwaysRequired) UnmarshalJSON([]byte) error { return errors.New("field name: required") }

// TestDecode_NestedDecoderErrors: an error from a value that decodes itself
// names the whole path from the top-level value, and an error the method
// writes itself reaches the client unchanged.
func TestDecode_NestedDecoderErrors(t *testing.T) {
	var b struct {
		Items []selfDecoding `json:"items"`
	}
	err := strictjson.Decode([]byte(`{"items":[{"name":"a"},{"Name":"b"}]}`), &b)
	if want := `invalid JSON at "/items/1": unknown member name "Name"`; err == nil || err.Error() != want {
		t.Errorf("got %v\nwant %s", err, want)
	}
	var r struct {
		Model alwaysRequired `json:"model"`
	}
	err = strictjson.Decode([]byte(`{"model":{}}`), &r)
	if want := `invalid JSON at "/model": field name: required`; err == nil || err.Error() != want {
		t.Errorf("got %v\nwant %s", err, want)
	}
}

// TestError_Under: a parser that decodes a member's raw JSON on its own (a
// group's conditions) places the member's error under the member's location.
func TestError_Under(t *testing.T) {
	var b struct{}
	err := strictjson.Decode([]byte(`{"x":1}`), &b)
	var de *strictjson.Error
	if !errors.As(err, &de) {
		t.Fatalf("got %T", err)
	}
	if got, want := de.Under("/conditions/1").Error(), `invalid JSON at "/conditions/1": unknown member name "x"`; got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
	if got, want := de.Error(), `invalid JSON at "": unknown member name "x"`; got != want {
		t.Errorf("Under changed the receiver: %s", got)
	}
	syntax := strictjson.Decode([]byte(`{`), &b).(*strictjson.Error)
	if got, want := syntax.Under("/conditions/1").Error(), `invalid JSON at "/conditions/1": unexpected EOF`; got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

// TestDecode_ErrorIsTyped: a caller tells a refused body from its own errors
// with errors.As.
func TestDecode_ErrorIsTyped(t *testing.T) {
	var b struct{}
	var de *strictjson.Error
	if err := strictjson.Decode([]byte(`{"x":1}`), &b); !errors.As(err, &de) {
		t.Fatalf("got %T", err)
	}
}
