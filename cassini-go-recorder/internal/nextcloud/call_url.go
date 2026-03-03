package nextcloud

import (
	"fmt"
	"net/url"
	"strings"
)

func ParseCallURL(raw string) (baseURL string, roomToken string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse call URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("invalid call URL %q", raw)
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "call" {
			token := strings.TrimSpace(parts[i+1])
			if token == "" {
				break
			}
			return u.Scheme + "://" + u.Host, token, nil
		}
	}

	return "", "", fmt.Errorf("could not extract room token from call URL %q", raw)
}
