package structs

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalidCursor is a cursor that this system did not issue, or that was altered.
var ErrInvalidCursor = errors.New("invalid cursor")

// cursorVersion prefixes the position inside the cursor, so the format can change without an old
// cursor being read as a new one.
const cursorVersion = "v1"

// EncodeCursor turns the position of the last item of a page into the opaque token a client hands
// back to get the next one. Opaque means the client is not supposed to read it, and that the
// position can become something else, a composite key or a timestamp, without breaking anyone.
func EncodeCursor(afterSeq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorVersion + "." + strconv.FormatInt(afterSeq, 10)))
}

// DecodeCursor reads a token made by EncodeCursor. An empty token is the start of the list.
func DecodeCursor(token string) (int64, error) {
	if token == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, fmt.Errorf("%w: not a token", ErrInvalidCursor)
	}
	version, position, found := strings.Cut(string(raw), ".")
	if !found || version != cursorVersion {
		return 0, fmt.Errorf("%w: unknown version", ErrInvalidCursor)
	}
	afterSeq, err := strconv.ParseInt(position, 10, 64)
	if err != nil || afterSeq < 0 {
		return 0, fmt.Errorf("%w: bad position", ErrInvalidCursor)
	}
	return afterSeq, nil
}
