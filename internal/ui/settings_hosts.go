package mcodeui

import (
	"strings"
	"unicode"
)

func parseHosts(raw string) []string {
	var hosts []string
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || strings.ContainsRune(p, ':') || strings.IndexFunc(p, unicode.IsSpace) >= 0 {
			continue
		}
		hosts = append(hosts, p)
	}
	return hosts
}
