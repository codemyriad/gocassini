# Structure: admin docs

## Audience

Someone deploying, operating, or troubleshooting the operator-backed Cassini stack.

## Outcome

An operator/admin should be able to:

- bring up the packaged stack
- understand what each service owns
- understand job, attempt, and publish behavior
- reason about storage, promotion, and failure isolation
- find the exact API/configuration/runtime details when needed

## Shape

This docset should mirror the developer-docs layered feel, but with operational concerns first.

Use this progression:

1. **Entry and reading paths**
   - `README.md`
   - fast paths based on intent
2. **Onboarding/task docs**
   - `start-here.md`
   - `quick-start.md`
3. **Explanation docs**
   - `system-overview.md`
   - `deployment-stack.md`
   - `operator-runtime.md`
   - `storage-and-promotion.md`
   - `day-2-operations.md`
4. **Reference docs**
   - `reference/README.md`
   - API, configuration, storage/filesystem, troubleshooting

## Include

- operator, control panel, and viewer roles
- Compose topology and browser surfaces
- storage boundaries and live-site promotion
- jobs, attempts, concurrency, reruns, and stop behavior
- practical start/inspect/recover flows
- operational limitations that affect expectations

## Exclude

- audience declarations inside the generated docs
- generation meta such as “this was generated from...”
- low-level media-processing internals unless they affect operations
- developer-oriented codebase tours

## Tone and framing

- Operational and pragmatic first.
- Explain the mental model before the full reference.
- Keep safe expectations visible: what is durable, what is retriable, what is not automatically resumed.
- Make failure boundaries and current limitations explicit.

## Candidate outputs

- `README.md`
- `start-here.md`
- `quick-start.md`
- `system-overview.md`
- `deployment-stack.md`
- `operator-runtime.md`
- `storage-and-promotion.md`
- `day-2-operations.md`
- `reference/*`

## Notes for the writer/agent

- The opening of each page should start with the operational content, not with an explanation of who the doc is for.
- The generated docset should stand on its own without pointing readers back to `docs/source-of-truth/`.
- Shape this docset as the admin/operations counterpart to the developer-docs flow.
