package operator

import (
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

var (
	noticeHostLabel  = regexp.MustCompile(`^[a-z0-9_-]+$`)
	noticeHostNumber = regexp.MustCompile(`^([0-9]+|0x[0-9a-f]*)$`)
)

// eventNoticeHost is the receiver's host as a meeting names it (#532), or ""
// for a host the operator will not name. Only hosts a browser writes exactly
// as typed, lowercased, are named, so the Webhooks panel's preview (the
// browser's own parsing) and the meeting agree: plain ASCII names, dotted
// decimal IPv4, and IPv6 in its compressed form. Never the port, path, query
// or credentials.
func eventNoticeHost(receiverURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(receiverURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" || addr.Is4In6() || addr.String() != host {
			return ""
		}
		return host
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	for _, label := range labels {
		if !noticeHostLabel.MatchString(label) {
			return ""
		}
	}
	if noticeHostNumber.MatchString(labels[len(labels)-1]) {
		return ""
	}
	return host
}
