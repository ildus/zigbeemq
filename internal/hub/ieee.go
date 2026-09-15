package hub

import (
	"strings"

	"github.com/marstid/goznp/pkg/znp"
)

func compactIEEE(s string) string {
	s = strings.ToLower(s)
	s = strings.TrimPrefix(s, "0x")
	return strings.ReplaceAll(s, ":", "")
}

func formatIEEE(addr [8]byte) string {
	return compactIEEE(znp.FormatIEEEAddr(addr))
}

// IEEE formats a coordinator/device address for JSON/UI.
func IEEE(addr [8]byte) string {
	return formatIEEE(addr)
}

func parseIEEE(s string) ([8]byte, error) {
	return znp.ParseIEEEAddr(compactIEEE(s))
}
