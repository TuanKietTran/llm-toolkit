package sources

import (
	"encoding/base64"
	"io"
	"strings"
)

// newBase64Reader decodes MIME base64 bodies, which are wrapped at 76 columns.
func newBase64Reader(r io.Reader) io.Reader {
	b, err := io.ReadAll(r)
	if err != nil {
		return strings.NewReader("")
	}
	clean := strings.Map(func(c rune) rune {
		if c == '\r' || c == '\n' || c == ' ' || c == '\t' {
			return -1
		}
		return c
	}, string(b))
	return base64.NewDecoder(base64.StdEncoding, strings.NewReader(clean))
}
