package mailer

import (
	"encoding/base64"
	"strings"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// wrap76 RFC 2045 base64 行宽 76 (CRLF)
func wrap76(s string) string {
	const width = 76
	if len(s) <= width {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); i += width {
		end := i + width
		if end > len(s) {
			end = len(s)
		}
		out.WriteString(s[i:end])
		if end != len(s) {
			out.WriteString("\r\n")
		}
	}
	return out.String()
}
