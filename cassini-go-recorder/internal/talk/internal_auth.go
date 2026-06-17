package talk

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func buildInternalHelloAuthParams(baseURL, internalSecret string) (map[string]any, error) {
	random, err := internalHelloRandom()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"random":  random,
		"token":   internalHelloToken(random, internalSecret),
		"backend": strings.TrimRight(strings.TrimSpace(baseURL), "/"),
	}, nil
}

func internalHelloToken(random, internalSecret string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(internalSecret)))
	_, _ = mac.Write([]byte(random))
	return hex.EncodeToString(mac.Sum(nil))
}

func internalHelloRandom() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate internal signaling random: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
