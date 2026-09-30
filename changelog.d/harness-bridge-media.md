### Changed
- Run the harness's Talk media services on the Compose network, reusing the existing nginx proxy for localhost callbacks instead of adding a service. Signaling shares the proxy's network namespace; AppAPI-only stacks now also reserve port 28082. macOS validation is pending, and simultaneous stacks still need separate ports and resource names.
- Stopped stacks using the previous proxy configuration must recreate containers with `cassini dev stack down` then `cassini dev stack up --resume` (same topology flags); data volumes are retained. Compose 2.17+ is required.
