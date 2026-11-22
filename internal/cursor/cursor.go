// Package cursor is the feed paging cursor "<created_at_unix_micro>_<id>".
package cursor

import (
	"fmt"
	"strconv"
	"strings"
)

// MaxMicro is 9999-12-31T23:59:59.999999Z, the latest timestamp Postgres compares safely.
// Cursors beyond it are invalid, so client input can never reach a database error.
const MaxMicro = 253402300799999999

// Cursor is the position of the last post of a page.
type Cursor struct {
	Micro int64
	ID    int64
}

func (c Cursor) String() string { return fmt.Sprintf("%d_%d", c.Micro, c.ID) }

// Parse accepts only two plain decimal numbers joined by "_" with Micro <= MaxMicro.
func Parse(s string) (Cursor, bool) {
	a, b, ok := strings.Cut(s, "_")
	if !ok || !digits(a) || !digits(b) {
		return Cursor{}, false
	}
	micro, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || micro > MaxMicro {
		return Cursor{}, false
	}
	return Cursor{micro, id}, true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
