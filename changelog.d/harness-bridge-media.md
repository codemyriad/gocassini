### Changed
- The local harness's Talk media services (signaling, NATS, Janus, Coturn) now run on the compose network with published ports instead of the host network. Docker Desktop's "Enable host networking" option is no longer needed on macOS, and several harness stacks can share one host once their published ports differ.
