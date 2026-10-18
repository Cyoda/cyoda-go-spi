// Package strictjson decodes a client's typed JSON — a request body, a
// condition — under one rule: a member name spells a field exactly, once.
//
// encoding/json (v1) matched a name to a field ignoring case and Unicode
// folding, and let a later duplicate overwrite or merge into an earlier one,
// so two readers of the same bytes could disagree on what a client sent.
// Decode is encoding/json/v2 with unknown members refused, and its error
// names the JSON location and never a Go type, so it is safe to put in a 400
// detail.
//
// Entity payloads are not typed JSON: in those, "a" and "A" are different
// fields.
package strictjson

import (
	"encoding"
	"encoding/base64"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// Decode decodes one JSON value into dst. A name that differs from a field's
// only in case, a duplicate name and an unknown name are each refused, as is
// anything but whitespace after the value. A refusal is an *Error.
//
// A type that decodes itself from an UnmarshalJSON method decodes through
// Decode too; its error then names the path from the top-level value.
func Decode(data []byte, dst any, opts ...Option) error {
	options := []json.Options{json.RejectUnknownMembers(true)}
	for _, o := range opts {
		options = append(options, o.options)
	}
	if err := json.Unmarshal(data, dst, options...); err != nil {
		return describe(err)
	}
	return nil
}

// Option changes one rule of Decode.
type Option struct{ options json.Options }

var (
	// PreserveNumbers makes a JSON number held in an `any`, at any depth, a
	// json.Number with the literal as written, instead of a float64 that
	// rounds an integer above 2^53.
	PreserveNumbers = Option{json.WithUnmarshalers(json.UnmarshalFromFunc(numberAsLiteral))}

	// AllowUnknownMembers ignores a member name the target does not declare,
	// for a probe that reads only some members and for a shape a standard
	// leaves open (a JWK). Names still match exactly, and a duplicate is
	// still refused.
	AllowUnknownMembers = Option{json.RejectUnknownMembers(false)}
)

func numberAsLiteral(dec *jsontext.Decoder, v *any) error {
	if dec.PeekKind() != '0' {
		return errors.ErrUnsupported // decoded as v2 decodes it
	}
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	*v = jsonv1.Number(tok.String())
	return nil
}

// Error is JSON Decode refused: where (a JSON Pointer, or a byte offset
// where the text is not JSON) and why.
type Error struct {
	at     jsontext.Pointer
	offset int64
	syntax bool
	class  string
	reason string
}

func (e *Error) Error() string {
	if e.syntax {
		return fmt.Sprintf("invalid JSON at offset %d: %s", e.offset, e.reason)
	}
	return fmt.Sprintf("invalid JSON at %q: %s", e.at, e.reason)
}

// Class names the kind of refusal — "not JSON", "unknown member",
// "duplicate member", "wrong kind of value" or "invalid value" — with none
// of the client's text, for a log line that must not carry it: a member name
// can be a tenant's data key.
func (e *Error) Class() string { return e.class }

// Under returns e placed under the JSON Pointer at, for a parser that decoded
// the value at that location on its own. A syntax error's byte offset is
// relative to that value, so the location replaces it.
func (e *Error) Under(at string) *Error {
	if e.syntax {
		return &Error{at: jsontext.Pointer(at), class: e.class, reason: e.reason}
	}
	return &Error{at: jsontext.Pointer(at) + e.at, class: e.class, reason: e.reason}
}

// Within places err under the JSON Pointer at when it is, or wraps, an
// *Error — for a caller that decoded the value at that location on its own —
// and returns any other error as it is.
func Within(err error, at string) error {
	var e *Error
	if errors.As(err, &e) {
		return e.Under(at)
	}
	return err
}

func describe(err error) *Error {
	var syn *jsontext.SyntacticError
	if errors.As(err, &syn) {
		if errors.Is(syn.Err, jsontext.ErrDuplicateName) {
			return &Error{at: syn.JSONPointer.Parent(), class: "duplicate member", reason: fmt.Sprintf("duplicate member name %q", syn.JSONPointer.LastToken())}
		}
		return &Error{syntax: true, offset: syn.ByteOffset, class: "not JSON", reason: syn.Err.Error()}
	}
	// Unmarshal of a byte slice reports every other failure as a
	// *json.SemanticError: a method's error is wrapped in one, and there is
	// no I/O to fail.
	var sem *json.SemanticError
	errors.As(err, &sem)
	var inner *Error
	var parse *time.ParseError
	var b64 base64.CorruptInputError
	switch {
	case errors.As(sem.Err, &inner):
		// A value that decoded itself through Decode: its location is
		// relative to that value.
		return inner.Under(string(sem.JSONPointer))
	case errors.Is(sem.Err, json.ErrUnknownName):
		return &Error{at: sem.JSONPointer.Parent(), class: "unknown member", reason: fmt.Sprintf("unknown member name %q", sem.JSONPointer.LastToken())}
	case errors.Is(sem.Err, strconv.ErrRange):
		return &Error{at: sem.JSONPointer, class: "invalid value", reason: "a JSON number out of range"}
	case errors.As(sem.Err, &parse):
		// time.ParseError quotes Go's reference layout, which is no use to
		// a client.
		return &Error{at: sem.JSONPointer, class: "invalid value", reason: "a JSON string where an RFC 3339 date-time is expected"}
	case errors.As(sem.Err, &b64):
		return &Error{at: sem.JSONPointer, class: "invalid value", reason: "a JSON string where base64 data is expected"}
	case sem.Err == nil, errors.Is(sem.Err, strconv.ErrSyntax),
		sem.JSONKind != '"' && textEncoded(sem.GoType):
		return &Error{at: sem.JSONPointer, class: "wrong kind of value", reason: fmt.Sprintf("%s where %s is expected", kindName(sem.JSONKind), expected(sem.GoType))}
	default:
		// An UnmarshalJSON method's own words.
		return &Error{at: sem.JSONPointer, class: "invalid value", reason: sem.Err.Error()}
	}
}

// kindName names the kind of a JSON value as a client reads it.
func kindName(k jsontext.Kind) string {
	switch k {
	case 'n':
		return "a JSON null"
	case 't', 'f':
		return "a JSON boolean"
	case '"':
		return "a JSON string"
	case '0':
		return "a JSON number"
	case '{':
		return "a JSON object"
	case '[':
		return "a JSON array"
	default:
		return "a JSON value"
	}
}

// expected names, in JSON terms, what a Go type accepts.
func expected(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "another value"
	}
	switch {
	case t == reflect.TypeFor[time.Time]():
		return "an RFC 3339 date-time string"
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8:
		return "a base64 string"
	case textEncoded(t):
		return "a string"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.String:
		return "a string"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	default:
		return "another value"
	}
}

// textEncoded reports whether t decodes from a JSON string through
// encoding.TextUnmarshaler.
func textEncoded(t reflect.Type) bool {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t != nil && reflect.PointerTo(t).Implements(reflect.TypeFor[encoding.TextUnmarshaler]())
}
