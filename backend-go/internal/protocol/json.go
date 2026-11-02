// Package protocol implements bounded JSON parsing and selected wire boundaries,
// not JSON Schema evaluation, authorization, or canonicalization.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid protocol input")

const MaxDepth = 64

// Decode retains numbers as json.Number. maxBytes is mandatory and measured
// before parsing. Errors deliberately do not include potentially secret input.
func Decode(data []byte, maxBytes int) (any, error) {
	if maxBytes <= 0 || len(data) > maxBytes || !utf8.Valid(data) || !validEscapes(data) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := value(d, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return v, nil
}
func value(d *json.Decoder, depth int) (any, error) {
	if depth > MaxDepth {
		return nil, ErrInvalid
	}
	t, e := d.Token()
	if e != nil {
		return nil, ErrInvalid
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, ErrInvalid
				}
				s, ok := k.(string)
				if !ok {
					return nil, ErrInvalid
				}
				if _, ok = m[s]; ok {
					return nil, ErrInvalid
				}
				v, e := value(d, depth+1)
				if e != nil {
					return nil, e
				}
				m[s] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, ErrInvalid
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, e := value(d, depth+1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, ErrInvalid
			}
			return a, nil
		default:
			return nil, ErrInvalid
		}
	default:
		return t, nil
	}
}

// encoding/json replaces lone UTF-16 surrogate escapes; reject before decoding.
// JSON syntax itself is checked by Decoder, not this escape scan.
func validEscapes(b []byte) bool {
	in := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			in = !in
			continue
		}
		if !in || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		n, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xDC00 && n <= 0xDFFF {
			return false
		}
		if n >= 0xD800 && n <= 0xDBFF {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}

// ExactObject is an explicit schema-call-site primitive: it does not infer
// fields from Go structs (encoding/json's case-insensitive matching is unsafe).
func ExactObject(v any, required, optional []string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrInvalid
	}
	allowed := map[string]bool{}
	for _, k := range required {
		allowed[k] = true
		if _, ok := m[k]; !ok {
			return nil, ErrInvalid
		}
	}
	for _, k := range optional {
		allowed[k] = true
	}
	for k := range m {
		if !allowed[k] {
			return nil, ErrInvalid
		}
	}
	return m, nil
}
