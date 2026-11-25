// Package ojson provides insertion-ordered JSON objects and Python-compatible serialisation.
//
// The 4D dependency manager's observable behaviour depends on the order in which declarations are met,
// so objects keep their key order. Numbers keep their source text (json.Number) so that they round-trip.
package ojson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// OMap is a JSON object that preserves insertion order. A duplicate key keeps its first position and takes the
// last value, as a Python dict does.
type OMap struct {
	keys []string
	m    map[string]any
}

func New() *OMap { return &OMap{m: map[string]any{}} }

func (o *OMap) Set(key string, value any) *OMap {
	if _, ok := o.m[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.m[key] = value
	return o
}

func (o *OMap) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.m[key]
	return v, ok
}

// Val returns the value of key, or nil when it is absent (Python's dict.get).
func (o *OMap) Val(key string) any {
	v, _ := o.Get(key)
	return v
}

func (o *OMap) Has(key string) bool { _, ok := o.Get(key); return ok }

func (o *OMap) Delete(key string) {
	if o == nil {
		return
	}
	if _, ok := o.m[key]; !ok {
		return
	}
	delete(o.m, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *OMap) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

func (o *OMap) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Copy returns a shallow copy.
func (o *OMap) Copy() *OMap {
	c := New()
	if o != nil {
		for _, k := range o.keys {
			c.Set(k, o.m[k])
		}
	}
	return c
}

func (o *OMap) MarshalJSON() ([]byte, error) { return Marshal(o, "") }

// String returns the value of key as a Go string when it is one.
func (o *OMap) String(key string) string {
	s, _ := o.Val(key).(string)
	return s
}

// ---------------------------------------------------------------- parsing

// Parse decodes one JSON document. Objects become *OMap, arrays []any, numbers json.Number.
func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("extra data after the JSON document")
		}
		return nil, err
	}
	return value, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := New()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key must be a string")
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(key, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			list := []any{}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return list, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		return tok, nil // string, json.Number, bool, nil
	}
}

// StripBOM removes a UTF-8 byte order mark (Python's utf-8-sig).
func StripBOM(data []byte) []byte { return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) }

// ---------------------------------------------------------------- semantics

// Truthy follows Python truthiness for JSON values.
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		f, err := t.Float64()
		return err != nil || f != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case []string:
		return len(t) > 0
	case *OMap:
		return t.Len() > 0
	}
	return true
}

// Str follows Python's str() for the value kinds a manifest can hold.
func Str(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case string:
		return t
	case json.Number:
		return t.String()
	}
	return PyDumps(v)
}

// Equal compares two JSON values as Python's == does: objects regardless of key order, numbers by value.
func Equal(a, b any) bool {
	switch x := a.(type) {
	case *OMap:
		y, ok := b.(*OMap)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.keys {
			bv, ok := y.m[k]
			if !ok || !Equal(x.m[k], bv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		if y, ok := b.(json.Number); ok {
			fx, e1 := x.Float64()
			fy, e2 := y.Float64()
			return e1 == nil && e2 == nil && fx == fy
		}
		return false
	}
	return a == b
}

// ---------------------------------------------------------------- serialisation

// Marshal writes v like Python's json.dumps(v, indent=indent, ensure_ascii=False). An empty indent gives the
// compact form with Python's default ", " and ": " separators.
func Marshal(v any, indent string) ([]byte, error) {
	var b bytes.Buffer
	if err := write(&b, v, indent, 0, false); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// PyDumps is json.dumps(v) with Python's defaults (ASCII escapes, ", " and ": " separators); it is used in
// messages, where it must read the same as the reference implementation's.
func PyDumps(v any) string {
	var b bytes.Buffer
	_ = write(&b, v, "", 0, true)
	return b.String()
}

func write(b *bytes.Buffer, v any, indent string, depth int, ascii bool) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case string:
		writeString(b, t, ascii)
	case json.Number:
		b.WriteString(t.String())
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return fmt.Errorf("unsupported number")
		}
		b.WriteString(strconv.FormatFloat(t, 'g', -1, 64))
	case []string:
		list := make([]any, len(t))
		for i, s := range t {
			list[i] = s
		}
		return write(b, list, indent, depth, ascii)
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteString(sep(indent))
			}
			newline(b, indent, depth+1)
			if err := write(b, item, indent, depth+1, ascii); err != nil {
				return err
			}
		}
		newline(b, indent, depth)
		b.WriteByte(']')
	case *OMap:
		if t == nil || len(t.keys) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteString(sep(indent))
			}
			newline(b, indent, depth+1)
			writeString(b, k, ascii)
			b.WriteString(": ")
			if err := write(b, t.m[k], indent, depth+1, ascii); err != nil {
				return err
			}
		}
		newline(b, indent, depth)
		b.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", v)
	}
	return nil
}

func sep(indent string) string {
	if indent != "" {
		return ","
	}
	return ", "
}

func newline(b *bytes.Buffer, indent string, depth int) {
	if indent == "" {
		return
	}
	b.WriteByte('\n')
	b.WriteString(strings.Repeat(indent, depth))
}

func writeString(b *bytes.Buffer, s string, ascii bool) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		case r < 0x80 || !ascii:
			b.WriteRune(r)
		case r >= 0x10000:
			r -= 0x10000
			fmt.Fprintf(b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
}

// Repr renders a value as Python's repr() does, for messages that quote a declaration value.
func Repr(v any) string {
	if s, ok := v.(string); ok {
		quote := byte('\'')
		if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
			quote = '"'
		}
		var b strings.Builder
		b.WriteByte(quote)
		for _, r := range s {
			switch {
			case r == rune(quote) || r == '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			case r == '\n':
				b.WriteString(`\n`)
			case r == '\r':
				b.WriteString(`\r`)
			case r == '\t':
				b.WriteString(`\t`)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte(quote)
		return b.String()
	}
	return Str(v)
}

// Int builds a JSON integer value.
func Int(n int) json.Number { return json.Number(strconv.Itoa(n)) }

// StrOrEmpty returns the string value of v, and "" for null or any non-string value.
func StrOrEmpty(v any) string { s, _ := v.(string); return s }
