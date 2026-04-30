# Cassini Venture Brief

Status: draft for partner discussion  
Date: 2026-03-12

## 1. Summary

Cassini is a meeting capture and post-processing product built around a simple promise: join a meeting, produce a reliable recording, turn it into a usable transcript and summary, and deliver a meeting artifact people can search, review, and keep in storage they control.

The strongest current shape is not "another conferencing platform." It is a Nextcloud-first recording and meeting-artifact system with room to expand into other meeting platforms, self-hosted deployments, and a managed EU-hosted service.

The first venture-level goal should be to prove that Cassini can become the best open-source-friendly way to capture and process meetings for privacy-conscious teams. The first product-level goal should be to define one credible MVP that is clearly "done" and useful enough to show pilots, partners, and potential funders.

## 2. Why This Matters

There is a real gap in the open-source ecosystem:

- reliable meeting capture is still weak compared with mainstream SaaS products
- teams using Nextcloud and adjacent self-hosted tools lack polished recording, transcript, and summary workflows
- political and regulatory pressure is increasing interest in EU-hosted and owned-storage workflows
- LLM summaries are only valuable if the capture pipeline is trustworthy and repeatable

Cassini is promising because it already has a real technical core, not just an idea. The opportunity is to turn that technical core into a product with a clear user, scope, and commercial story.

## 3. Who Cassini Is For

Primary early user:

- privacy-conscious teams already using Nextcloud Talk, or close to that ecosystem, who want meeting recordings, transcripts, and summaries without handing everything to a US SaaS platform

Likely early buyers or champions:

- Nextcloud administrators
- small and medium organizations with self-hosting preferences
- consultancies, agencies, or internal teams that need meeting memory and searchable transcripts
- open-source-oriented organizations that value owned storage and deployment flexibility

Not the first target:

- broad consumer meeting users
- teams that need Cassini to replace Zoom, Meet, or Teams end to end
- customers who primarily want a full workflow automation platform before the core artifact flow is proven

## 4. Repo-Grounded Project State

Cassini is already three concrete components in one repository:

- `cassini-go-recorder`: a Go recorder focused on Nextcloud Talk capture, deterministic artifacts, and offline remux
- `cassini-transcriber`: a Python post-processing pipeline that turns meeting recordings into a transcript artifact with captions, manifest data, and optional LLM-cleaned readable output
- `cassini-viewer`: a static Svelte viewer that renders an artifact, supports click-to-seek playback, and provides simple transcript search

What looks real today:

- the recorder already has a strong artifact contract: `.mkv`, `.csr`, per-session logs, JSON reports, and deterministic remux tooling
- the transcriber already assumes a real meeting-artifact pipeline rather than a one-off script
- the viewer already points toward the "single meeting page" idea and can be statically shipped with an artifact directory
- the repo includes a reproducible local Nextcloud Talk test harness, not just unit tests

What this means strategically:

- the recording component is the most mature part and is already at or near MVP quality for its current narrow scope
- transcription and readable-output work are meaningful second-stage capabilities, not just speculation
- the viewer is a credible artifact delivery surface, but not yet a full recordings library or product app

What is clearly not product-complete yet:

- no operator-facing control plane for "tell the bot where to go"
- no calendar workflow, scheduling layer, or general orchestration product
- no production-grade library for managing many meetings across time
- no cross-meeting semantic search or RAG query system
- no proven Google Meet or Jitsi adapter in the current repo
- no hosted service layer, billing, multi-tenant auth, or admin product
- no packaged Nextcloud app/store presence yet

## 5. Product Thesis

Cassini should become the privacy-respecting meeting memory layer for teams that want owned recordings and useful post-processing.

The product thesis is:

- reliable capture is the hard part and creates trust
- transcript and summary make the recording usable
- a lightweight artifact page makes the result easy to share and revisit
- owned storage and EU-hosted options are strategic differentiators
- starting with Nextcloud gives Cassini a focused wedge into an ecosystem that is underserved

## 6. Proposed First MVP

The first MVP should stay narrow.

Recommended MVP definition:

"Cassini records a Nextcloud Talk meeting, produces a transcript and summary, stores the outputs in a Nextcloud-friendly location, and delivers a single meeting page that people can open, search, and share."

Recommended MVP scope:

- one supported meeting platform: Nextcloud Talk
- one reliable capture path: recorder bot joins and records the meeting
- one post-processing path: transcription plus readable summary
- one delivery artifact: a static meeting page with audio, transcript, captions, and summary
- one storage story: artifacts live in owned storage, with Nextcloud as the primary integration target
- one lightweight way to trigger recording: manual operator flow first, then a simple command or integration if needed

Recommended MVP done criteria:

- a real Nextcloud Talk meeting can be recorded end to end without manual repair of the output
- the output includes a final recording, transcript, captions, and a readable meeting page
- the transcript is stored somewhere users already control and can inspect
- basic search works on the meeting output
- setup documentation explains hardware and model expectations clearly
- the flow is solid enough to show as a pilot and use in a funding conversation

What this MVP should prove:

- users actually want the artifact, not just the raw recording
- Nextcloud-first is a strong enough wedge to get early adoption
- local-first or owned-storage positioning resonates
- there is a credible path to either funding, pilots, or paid managed deployments

## 7. What Should Wait Until After MVP

These are good directions, but they should not define "done" for the first target:

- Google Meet support
- Jitsi support
- cross-platform orchestration adapters
- semantic chat or deep RAG over the full archive
- video snippet generation and highlight composition
- polished multi-meeting asset library with advanced filtering
- full calendar automation
- multi-tenant hosted control plane
- billing and freemium mechanics
- sound design and launch polish as a gating dependency

Some of these can be run as experiments in parallel, but they should not be required to declare the first MVP complete.

## 8. Business Model Hypotheses

The repo does not force one business model yet, which is good. The likely options are:

- open core plus paid managed service
- self-hostable product with paid support and deployment work
- EU-hosted managed service for organizations that want privacy positioning without self-hosting
- a funding-backed product push starting from a strong Nextcloud/open-source wedge

Recommended stance for now:

- keep the product architecture compatible with both self-hosted and managed deployment
- avoid locking the repo into a purely SaaS assumption
- treat the commercial question as a decision to answer during MVP shaping, not before capture-to-artifact value is proven

## 9. Recommended Near-Term Direction

1. Keep the near-term product frame Nextcloud-first, not platform-agnostic.
2. Define the first MVP around one end-to-end outcome: meeting to artifact page.
3. Treat the recorder as the current anchor component and shape the rest around its artifact contract.
4. Use the existing static viewer as the starting point for the "single meeting page" deliverable.
5. Store transcripts and artifacts in a Nextcloud-compatible way early, even if the broader library UX comes later.
6. Keep the repository together until the core contracts stabilize.
7. Split repos only when deployment cadence or ownership boundaries become painful.

That last point matters: today the recorder, transcriber, and viewer are still converging on shared artifact contracts. Splitting too early will add coordination cost before the interfaces are stable.

## 10. Questions We Need To Answer

These are the main venture questions still open.

Product and user:

- Is Cassini primarily a Nextcloud product, or a broader meeting product that happens to start with Nextcloud?
- Who is the first real user: admin/operator, end user, or organization buyer?
- What user behavior are we trying to win first: reliable recording, searchable transcript, summary delivery, or archive/library access?

Commercial:

- Is the first objective funding from Futto, pilot adoption, or a sellable service?
- Do we want open-source distribution to be a growth channel, a trust signal, or the main product model?
- Do we believe the first commercial offer is self-hosted support, managed EU hosting, or both?

MVP scope:

- What is the minimum summary output for v1: plain transcript, readable cleaned transcript, or true executive summary?
- Does "search" in v1 mean within one meeting page only, or across many meetings?
- Does the MVP need a real control surface for starting recordings, or is an operator/manual flow enough?
- Is attaching a single HTML meeting page after the meeting the right primary deliverable?

Platform and architecture:

- Should Nextcloud be the source of truth for storage, or only an integration target?
- What belongs in this repo through MVP, and what is worth splitting later?
- What level of model flexibility do we want in practice: local only, API only, or hybrid?
- What are the minimum supported hardware expectations for a realistic pilot deployment?

Risk and compliance:

- What consent and notification model is required when the bot joins a meeting?
- What retention and deletion story do we need from day one?
- Which parts must stay in the EU for us to make a strong hosting claim?

Success criteria:

- What exact conditions make the MVP "done"?
- How many pilot users or organizations do we need before the next investment in scope?
- What reliability bar is good enough for early external demos?

## 11. Proposal Documents To Write Next

Once this venture brief is agreed, the next step should be a small set of shaped proposals:

- capture and orchestration
- transcription and summarization
- artifact page, library, and search
- Nextcloud integration and store packaging
- deployment model and hosted-service shape

Use one template for each proposal:

- objective
- target user and problem
- in-scope work
- out-of-scope work
- dependencies and interfaces
- acceptance criteria
- risks and unknowns
- decisions needed from the partners

## 12. Suggested Working Position

If we need a default working position until more decisions are made, it should be this:

Cassini is a Nextcloud-first meeting recorder and artifact system for privacy-conscious teams. The first MVP is an end-to-end meeting-to-artifact flow: record the meeting, transcribe it, summarize it, store it in owned storage, and deliver one meeting page people can use immediately. Everything else should support proving that outcome.
