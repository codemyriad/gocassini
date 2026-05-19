# Structure: developer docs

## Audience

Developers changing code in this repo.

## Outcome

A developer should be able to:

- get Cassini running locally
- understand the product/runtime shape before diving into code
- find the right subsystem quickly
- understand which artifact contracts and boundaries matter before making changes

## Shape

This docset should feel like a top-down developer guide rather than a single reference page.

Use this progression:

1. **Entry and reading paths**
   - `README.md`
   - fast paths based on intent
2. **Onboarding/task docs**
   - `start-here.md`
   - `quick-start.md`
3. **Explanation docs**
   - `mental-model.md`
   - `local-developer-stack.md`
   - `operator-stack.md`
   - `core-pipeline.md`
4. **Component docs**
   - `components/README.md`
   - focused component pages
5. **Reference docs**
   - `reference/README.md`
   - API, configuration, artifacts/filesystem, glossary, troubleshooting

## Include

- the happy path to a visible end-to-end result
- the artifact pipeline and operating modes
- component and runtime boundaries
- local harness and deployment-bundle workflows
- operator/runtime semantics that affect development
- concrete commands and repo paths
- cross-links downward from overview pages into deeper pages

## Exclude

- audience declarations inside the generated docs
- generation meta such as “this was generated from...”
- admin-only operational detail that does not help code changes
- large dumps of raw implementation detail on the first pages

## Tone and framing

- Quick success first.
- Conceptual depth later.
- Reference last.
- Assume technical comfort, but do not assume prior Cassini vocabulary.
- Introduce terms like `.run`, `.meeting`, `.opus`, `attempt`, and `current/` before relying on them heavily.

## Candidate outputs

- `README.md`
- `start-here.md`
- `quick-start.md`
- `mental-model.md`
- `local-developer-stack.md`
- `operator-stack.md`
- `core-pipeline.md`
- `components/*`
- `reference/*`

## Notes for the writer/agent

- The opening of each page should start with the content, not with an explanation of who the doc is for.
- The generated docset should stand on its own without pointing readers back to `docs/source-of-truth/`.
- Prefer the page map and reading flow found in `dev-docs-wip/`.
