package gatewaywire

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/protocol"
)

type object map[string]any

// Only internally projected objects reach this encoder. ASCII keys have the
// same order in UTF-8/UTF-16. No float64, HTML escaping or generic-JCS promise.
func canonical(v any) ([]byte, error) { return appendCanonical(nil, v) }
func appendCanonical(dst []byte, v any) ([]byte, error) {
	switch x := v.(type) {
	case string:
		if !utf8.ValidString(x) {
			return nil, ErrInvalid
		}
		dst = append(dst, '"')
		for _, r := range x {
			switch r {
			case '"', '\\':
				dst = append(dst, '\\', byte(r))
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\t':
				dst = append(dst, '\\', 't')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\r':
				dst = append(dst, '\\', 'r')
			default:
				if r < 0x20 {
					dst = append(dst, '\\', 'u', '0', '0', "0123456789abcdef"[r>>4], "0123456789abcdef"[r&15])
				} else {
					dst = utf8.AppendRune(dst, r)
				}
			}
		}
		return append(dst, '"'), nil
	case int64:
		if x < 0 || x > protocol.MaxSafeInteger {
			return nil, ErrInvalid
		}
		return strconv.AppendInt(dst, x, 10), nil
	case bool:
		return strconv.AppendBool(dst, x), nil
	case []string:
		dst = append(dst, '[')
		for i, s := range x {
			if i > 0 {
				dst = append(dst, ',')
			}
			var e error
			dst, e = appendCanonical(dst, s)
			if e != nil {
				return nil, ErrInvalid
			}
		}
		return append(dst, ']'), nil
	case object:
		keys := make([]string, 0, len(x))
		for k := range x {
			for _, b := range []byte(k) {
				if b > 127 {
					return nil, ErrInvalid
				}
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		dst = append(dst, '{')
		for i, k := range keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			var e error
			dst, e = appendCanonical(dst, k)
			if e != nil {
				return nil, ErrInvalid
			}
			dst = append(dst, ':')
			dst, e = appendCanonical(dst, x[k])
			if e != nil {
				return nil, ErrInvalid
			}
		}
		return append(dst, '}'), nil
	default:
		return nil, ErrInvalid
	}
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
