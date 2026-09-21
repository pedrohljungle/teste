package structs

import "time"

// timestampLayout is RFC 3339 in UTC with milliseconds, the format of every timestamp the API and
// the events return.
const timestampLayout = "2006-01-02T15:04:05.000Z"

// Timestamp formats an instant for a response. The zero time, which the domain uses for "has not
// happened", is the empty string, so a field that does not apply can be left out.
func Timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timestampLayout)
}
