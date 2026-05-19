# Structure: microsite docs

## Audience

A broader technical audience that wants to understand the product and system without reading the whole repo.

## Outcome

They should leave with a clear picture of what Cassini does, how the pipeline works at a high level, what the major surfaces are, and how to explore further.

## Suggested flow

1. Home / product overview
2. How it works
3. Product surfaces
4. Deployment model
5. Limitations or current scope
6. Deeper technical links

This structure can become either a single long page or a small set of linked pages.

## Include

- the core meeting pipeline story
- the portable `.opus` product narrative
- the distinction between operator/control panel/viewer
- a clean explanation of why durable artifacts matter
- links outward to developer/admin detail when appropriate

## Exclude

- repo-maintainer minutiae
- internal-only debugging surfaces unless they explain the system clearly
- implementation trivia not useful to an external technical reader

## Tone and framing

Clear, product-aware, and technically credible. Fewer internals than the developer docs, but still specific.

## Candidate outputs

- a microsite home page
- a site map / page plan
- later, separate pages such as `how-it-works.md` or `deployment.md`

## Notes for the writer/agent

Prefer a coherent story over a dump of facts. When several details compete for attention, keep the user-visible product path first and move implementation details later.
