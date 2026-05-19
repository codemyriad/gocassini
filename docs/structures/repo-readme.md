# Structure: repo README

## Audience

A new developer, technical evaluator, or agent landing on the repo root.

## Outcome

They should understand what Cassini is, what the primary product flow is, how to run the first commands, and where to go next.

## Suggested flow

1. One-paragraph product summary
2. Primary user flow
3. First commands
4. Repo map at the component level
5. Common local flows
6. Constraints or caveats worth knowing early
7. Docs map

## Include

- the `./bin/cassini` entrypoint
- the record/build/publish mental model
- the portable `.opus` story
- a short map of major top-level directories
- links to deeper docs

## Exclude

- deep operator internals
- exhaustive deployment runbooks
- low-level artifact semantics unless needed for orientation

## Tone and framing

Fast, factual, and welcoming. Optimize for orientation over completeness.

## Candidate outputs

- one `README.md`
- optionally a short companion repo map later

## Notes for the writer/agent

Keep it skimmable. If the source offers several valid stories, prefer the current product surface over legacy implementation history.
