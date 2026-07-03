# Security Policy

## Supported Versions

Security fixes are handled on the latest released version of Cassini. Until the
first public app-store release is cut, fixes land on `main`.

## Reporting a Vulnerability

Please do not open a public issue for a suspected vulnerability.

Report security issues by emailing:

security@codemyriad.io

Include the affected version or commit, deployment mode, a description of the
impact, and reproduction steps if available. We will acknowledge valid reports
within 5 business days and coordinate a fix or mitigation before public
disclosure.

## Secrets and Test Fixtures

The repository contains deterministic credentials in local harness and test
configuration. These values are for isolated development and CI environments
only. They are not production credentials and must not be reused in a real
Nextcloud deployment.

Production deployments must generate fresh values for AppAPI, Talk recording,
Talk signaling, TURN, LLM provider, and storage credentials. See
`docs/exapp-install.md` for the production configuration flow.
