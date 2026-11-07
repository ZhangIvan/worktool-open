// Package auth parses transport credentials; current authorization is always
// checked by the PG store, not by these syntax helpers or request tenant fields.
package auth

import (
	"errors"
	"net/http"
	"strings"
)

var ErrUnauthenticated = errors.New("UNAUTHENTICATED")

func Bearer(r *http.Request, app bool) (string, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", ErrUnauthenticated
	}
	v := strings.TrimPrefix(values[0], "Bearer ")
	if app {
		if len(v) != 64 {
			return "", ErrUnauthenticated
		}
	} else if len(v) < 32 || len(v) > 256 {
		return "", ErrUnauthenticated
	}
	for _, c := range v {
		good := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if app {
			good = c >= 'a' && c <= 'f' || c >= '0' && c <= '9'
		}
		if !good {
			return "", ErrUnauthenticated
		}
	}
	return v, nil
}
