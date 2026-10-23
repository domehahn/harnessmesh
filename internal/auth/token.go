package auth

import (
	"crypto/subtle"
	"fmt"
	"os"
	"strings"
)

// TokenValue returns the direct token when configured, otherwise the current
// contents of a mounted secret file. Reading the file on demand makes an
// atomic file replacement an effective rotation without a service restart.
func TokenValue(direct, file string) (string, error) {
	if strings.TrimSpace(direct) != "" {
		return strings.TrimSpace(direct), nil
	}
	file = strings.TrimSpace(file)
	if file == "" {
		return "", nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// MatchToken compares a presented bearer token to the current configured
// value and fails closed when the file cannot be read or is empty.
func MatchToken(direct, file, presented string) bool {
	configured, err := TokenValue(direct, file)
	if err != nil || configured == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(presented)) == 1
}
