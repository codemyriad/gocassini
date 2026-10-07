package talk

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"syscall"

	"gocassini/internal/nextcloud"
)

type unreachableFinding struct {
	code    string
	message string
	steps   []ConnectionStep
}

const nextcloudAddressStep = "Cassini reaches Nextcloud at `NEXTCLOUD_URL`, or at `CASSINI_TALK_BACKEND_URL` when that is set. The address has to work from inside Cassini's container, not only from your browser"

var nextcloudTestStep = ConnectionStep{
	Label:    "To test the address, run this on the Nextcloud host. It should print an HTTP status such as 200, not an error:",
	Commands: []string{`docker exec nc_app_gocassini sh -lc 'curl -k -s -o /dev/null -w "%{http_code}\n" "$NEXTCLOUD_URL/status.php"'`},
}

func classifyUnreachable(err error) unreachableFinding {
	var certErr *tls.CertificateVerificationError
	var authorityErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidErr x509.CertificateInvalidError
	if errors.As(err, &certErr) || errors.As(err, &authorityErr) || errors.As(err, &hostnameErr) || errors.As(err, &invalidErr) {
		return unreachableFinding{
			code:    "nextcloud_tls_untrusted",
			message: "Cassini reached Nextcloud but does not trust its certificate, so it did not read Talk's settings.",
			steps: []ConnectionStep{
				{Label: "A self-signed or private certificate is not trusted from inside Cassini's container. Use a publicly trusted certificate on Nextcloud, or point `CASSINI_TALK_BACKEND_URL` at an address whose certificate Cassini trusts"},
				{Label: "The address Cassini uses must also match the name the certificate was issued for"},
			},
		}
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return unreachableFinding{
			code:    "nextcloud_host_not_found",
			message: "Cassini could not find Nextcloud at the address it was given: the name does not resolve from Cassini's container.",
			steps:   []ConnectionStep{{Label: nextcloudAddressStep}, nextcloudTestStep},
		}
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return unreachableFinding{
			code:    "nextcloud_connection_refused",
			message: "Nothing answered at the Nextcloud address Cassini was given: the connection was refused.",
			steps: []ConnectionStep{
				{Label: "Check that Nextcloud is running and listening on that address and port"},
				{Label: nextcloudAddressStep},
				nextcloudTestStep,
			},
		}
	}

	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return unreachableFinding{
			code:    "nextcloud_timeout",
			message: "Nextcloud did not answer in time at the address Cassini was given.",
			steps: []ConnectionStep{
				{Label: "Check that Nextcloud is running, and is not overloaded or part-way through a restart"},
				{Label: "Check that no firewall between Cassini's container and Nextcloud drops the connection"},
				nextcloudTestStep,
			},
		}
	}

	var ocsErr *nextcloud.OCSError
	if errors.As(err, &ocsErr) && ocsErr.HTTPStatus >= 500 {
		return unreachableFinding{
			code:    "nextcloud_server_error",
			message: "Nextcloud returned a server error while Cassini was reading Talk's settings.",
			steps: []ConnectionStep{
				{Label: "Nextcloud's log says what failed: Administration settings → Logging, or nextcloud.log on the server"},
			},
		}
	}

	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return unreachableFinding{
			code:    "nextcloud_unexpected_response",
			message: "Something answered at the Nextcloud address, but not with Talk's settings.",
			steps: []ConnectionStep{
				{Label: "Nextcloud may be in maintenance mode or still starting. Wait for it, then check again"},
				{Label: "A proxy may be answering in Nextcloud's place. Make sure `NEXTCLOUD_URL`, or `CASSINI_TALK_BACKEND_URL` when set, points at Nextcloud itself"},
			},
		}
	}

	return unreachableFinding{
		code:    "nextcloud_unreachable",
		message: "Could not read Talk settings. Check Nextcloud connectivity and TLS, then try again.",
		steps:   []ConnectionStep{{Label: nextcloudAddressStep}, nextcloudTestStep},
	}
}
