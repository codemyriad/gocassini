package meetingtime

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	doubledDashStampPattern  = regexp.MustCompile(`^(.*)--(\d{4})-(\d{2})-(\d{2})--(\d{2}):(\d{2})(?::(\d{2}))?$`)
	embeddedDateStampPattern = regexp.MustCompile(`^(.*)-(\d{4})-(\d{2})-(\d{2})--(\d{2}):(\d{2})(?::(\d{2}))?$`)
	compactStampPattern      = regexp.MustCompile(`^(.*)--(\d{8})T(\d{2})(\d{2})(\d{2})$`)
)

// InferRecordedAtLocal returns a stable local wall-clock timestamp derived from
// a meeting artifact name when it follows one of Cassini's timestamped naming
// conventions. The returned format is YYYY-MM-DDTHH:MM:SS without a timezone.
func InferRecordedAtLocal(path string) string {
	base := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if base == "" {
		return ""
	}

	if matches := doubledDashStampPattern.FindStringSubmatch(base); matches != nil {
		seconds := matches[7]
		if seconds == "" {
			seconds = "00"
		}
		return matches[2] + "-" + matches[3] + "-" + matches[4] + "T" + matches[5] + ":" + matches[6] + ":" + seconds
	}
	if matches := embeddedDateStampPattern.FindStringSubmatch(base); matches != nil {
		seconds := matches[7]
		if seconds == "" {
			seconds = "00"
		}
		return matches[2] + "-" + matches[3] + "-" + matches[4] + "T" + matches[5] + ":" + matches[6] + ":" + seconds
	}
	if matches := compactStampPattern.FindStringSubmatch(base); matches != nil {
		return matches[2][0:4] + "-" + matches[2][4:6] + "-" + matches[2][6:8] + "T" + matches[3] + ":" + matches[4] + ":" + matches[5]
	}
	return ""
}
