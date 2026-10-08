package talk

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"gocassini/internal/nextcloud"
)

func requestFailed(cause error) error {
	return fmt.Errorf("request GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings: %w",
		&url.Error{Op: "Get", URL: "https://cloud.invalid/ocs", Err: cause})
}

func TestAnUnreachableNextcloudSaysWhichFailureItWas(t *testing.T) {
	var notJSON any
	syntaxErr := json.Unmarshal([]byte("<html>502 Bad Gateway</html>"), &notJSON)
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"untrusted certificate", requestFailed(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}), "nextcloud_tls_untrusted"},
		{"certificate for another name", requestFailed(x509.HostnameError{Host: "cloud.invalid"}), "nextcloud_tls_untrusted"},
		{"name does not resolve", requestFailed(&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "cloud.invalid", IsNotFound: true}}), "nextcloud_host_not_found"},
		{"connection refused", requestFailed(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}), "nextcloud_connection_refused"},
		{"deadline exceeded", requestFailed(context.DeadlineExceeded), "nextcloud_timeout"},
		{"server error with an OCS body", &nextcloud.OCSError{HTTPStatus: 503}, "nextcloud_server_error"},
		{"a proxy's HTML page", fmt.Errorf("decode OCS envelope GET /ocs: %w", syntaxErr), "nextcloud_unexpected_response"},
		{"anything else", errors.New("connection reset"), "nextcloud_unreachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyUnreachable(c.err)
			if got.code != c.code {
				t.Fatalf("code = %q, want %q", got.code, c.code)
			}
			if got.message == "" || len(got.steps) == 0 {
				t.Fatalf("%s says nothing the reader can act on: %+v", c.code, got)
			}
		})
	}
}

func TestAClientErrorIsNotAServerError(t *testing.T) {
	if got := classifyUnreachable(&nextcloud.OCSError{HTTPStatus: 429}); got.code != "nextcloud_unreachable" {
		t.Fatalf("a 429 classified as %q; only 5xx is Nextcloud failing", got.code)
	}
}
