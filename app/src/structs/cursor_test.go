package structs

import (
	"errors"
	"strings"
	"testing"
)

func TestACursorRoundTripsThePosition(t *testing.T) {
	for _, position := range []int64{0, 1, 50, 9_223_372_036_854_775_807} {
		got, err := DecodeCursor(EncodeCursor(position))
		if err != nil || got != position {
			t.Errorf("position %d came back as %d, %v", position, got, err)
		}
	}
}

func TestAnEmptyCursorIsTheStartOfTheList(t *testing.T) {
	got, err := DecodeCursor("")
	if err != nil || got != 0 {
		t.Fatalf("DecodeCursor(\"\") = %d, %v", got, err)
	}
}

func TestACursorIsOpaqueAndSafeInAURL(t *testing.T) {
	token := EncodeCursor(12345)
	for _, forbidden := range []string{"12345", "v1", "=", "+", "/"} {
		if strings.Contains(token, forbidden) {
			t.Fatalf("token %q exposes or is unsafe with %q", token, forbidden)
		}
	}
}

func TestACursorThatWasNotIssuedHereIsRefused(t *testing.T) {
	cases := map[string]string{
		"not base64":        "!!!not a token!!!",
		"unknown version":   "djIuMTA",
		"no separator":      "dm9uZQ",
		"a text position":   "djEuYWJj",
		"a negative number": "djEuLTU",
	}
	for name, token := range cases {
		if _, err := DecodeCursor(token); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: error = %v, want ErrInvalidCursor", name, err)
		}
	}
}
