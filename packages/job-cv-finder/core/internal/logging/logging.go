// Package logging builds a JSON slog logger that redacts secret-looking attributes, so a
// careless log call cannot leak a credential, cookie or token.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

var sensitive = []string{"secret", "password", "passwd", "token", "cookie", "authorization", "credential", "api_key", "apikey", "session"}

// Redact reports whether an attribute key must be redacted.
func Redact(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitive {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// New returns a JSON logger at the given level with redaction.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if Redact(a.Key) {
			return slog.String(a.Key, "[REDACTED]")
		}
		return a
	}}))
}
