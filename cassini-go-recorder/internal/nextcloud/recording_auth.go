package nextcloud

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
)

const (
	talkRecordingRandomHeader   = "Talk-Recording-Random"
	talkRecordingChecksumHeader = "Talk-Recording-Checksum"
)

func talkRecordingAuthHeaders(secret string, body []byte) (http.Header, error) {
	random, err := talkRecordingRandom()
	if err != nil {
		return nil, err
	}
	headers := make(http.Header, 2)
	headers.Set(talkRecordingRandomHeader, random)
	headers.Set(talkRecordingChecksumHeader, talkRecordingChecksum(secret, random, body))
	return headers, nil
}

func talkRecordingChecksum(secret, random string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(random))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func talkRecordingRandom() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate talk recording random: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
