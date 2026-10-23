package config

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

const minimumStrongBearerTokenBytes = 32

// RequireStrongBearerTokens reports whether network services must reject
// short bearer tokens. Production Compose overlays enable this explicitly.
func RequireStrongBearerTokens() bool {
	return strings.TrimSpace(os.Getenv("HARNESSMESH_REQUIRE_STRONG_TOKENS")) == "1"
}

func ValidateBearerToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("bearer token must not be empty")
	}
	if strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return fmt.Errorf("bearer token must not contain whitespace")
	}
	if RequireStrongBearerTokens() && len([]byte(token)) < minimumStrongBearerTokenBytes {
		return fmt.Errorf("bearer token must be at least %d bytes when strong-token policy is enabled", minimumStrongBearerTokenBytes)
	}
	return nil
}
