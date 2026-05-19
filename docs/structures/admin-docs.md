# Structure: admin docs

## Audience

Someone deploying, operating, or troubleshooting the operator-backed Cassini stack.

## Outcome

They should understand the deployed topology, the runtime flow, the persistent storage model, the main configuration knobs, and the operational consequences of the current design.

## Suggested flow

1. What the deployed stack contains
2. Service topology and boundaries
3. Runtime job flow from trigger to published site
4. Storage and persistence model
5. Configuration surface
6. Common operational tasks
7. Failure behavior and current limitations
8. Links to deeper operator/deployment reference

## Include

- operator, control panel, and viewer roles
- shared storage and live-site promotion model
- jobs, attempts, reruns, and publish serialization
- Docker Compose quickstart and public surfaces
- operational constraints that affect expectations

## Exclude

- detailed UI walkthroughs unless they affect operations
- low-level media-processing internals
- marketing/product framing that does not help run the system

## Tone and framing

Operational, pragmatic, and expectation-setting. Prioritize safe mental models over exhaustive implementation detail.

## Candidate outputs

- an admin landing page
- later, a runbook or troubleshooting page if needed

## Notes for the writer/agent

Assume the reader cares about what runs where, what is durable, what can be retried, and what failure looks like. Keep the distinction between control-plane behavior and media-processing behavior clear.
