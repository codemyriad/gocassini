---
title: AI providers, summaries and insights
description: Register an OpenAI-compatible endpoint in the app, decide what each step uses, and ask a question of the meetings you pick.
source: docs/reference/configuration.md
copied: "2026-09-17"
---

Nothing here happens until you configure a language model. Once you do,
transcript text goes to it in two cases: automatically, to summarise each
meeting, and on request, when someone asks a question about meetings they have
access to.

The first is a **summary**, written when a meeting publishes. The second is an
**insight**, a question somebody asks of meetings they pick. Both are the same
act underneath — one call to an OpenAI-compatible endpoint — and both send text,
never audio. Recording and transcription stay on your own hardware either way.

## Registering an endpoint

Endpoints are a setting in the app, under **Operator › Settings › AI providers**.
Adding one is a form: a name, the API base URL, an optional key, an optional
default model, and the request bounds.

- **A base URL is what switches the calls on.** An API key is optional, so a
  keyless self-hosted OpenAI-compatible endpoint works. With no key configured
  Cassini omits the `Authorization` header entirely rather than sending an empty
  bearer token, which some self-hosted servers reject.
- **Give the API root**, not a specific route. A base URL ending in
  `/chat/completions` or `/models` is refused with a hint.
- **Each provider carries a default model.** Summaries and insights use it
  unless a step names its own, and the model field in the app opens pre-filled
  with it. A saved endpoint reports whether its model list actually came back;
  an endpoint with no `/models` route still answers completions, and is not
  treated as broken.
- **Registering your first endpoint switches meeting summaries on**, pointed at
  it. That happens once in a deployment's life, so an administrator who turns
  summarising off keeps it off.

The rule to hold on to: **every endpoint you register is one an insight may
reach.** There is no separate switch that turns insights off while an endpoint
is registered; removing the endpoints is how they are turned off.

`LLM_BASE_URL`, `LLM_MODEL` and `OPENROUTER_API_KEY` in the deploy environment
only **seed** these settings on the app's first start. After that the stored
settings are what every build receives, so moving between a hosted provider and
your own model server is one settings change with no redeploy.

## Per-step endpoints

Each step can be pointed at its own endpoint, overriding the shared values. The
steps are `SUMMARY` (the summary written when a meeting publishes) and `INSIGHT`
(a question asked of selected meetings).

| Variable | Purpose |
|---|---|
| `<STEP>_BASE_URL` | this step's endpoint, replacing the shared one |
| `<STEP>_API_KEY` | this step's key |
| `<STEP>_MODEL` | this step's model |
| `<STEP>_TIMEOUT_SEC` | this step's request timeout, replacing `CASSINI_LLM_TIMEOUT_SEC` |
| `<STEP>_MAX_TOKENS` | this step's response token limit, replacing `CASSINI_LLM_MAX_TOKENS` |

An endpoint override brings its own key: setting `<STEP>_BASE_URL` without
`<STEP>_API_KEY` sends no key at all rather than the shared one, because a key
must never travel to a host it was not issued for. A model or a bound on its own
keeps whatever endpoint it is layered over.

`INSIGHT_*` layers over `SUMMARY_*`, not just over the shared values, so a
deployment that only ever configured a summary endpoint can still run insights —
they run on the summary endpoint. Set `INSIGHT_BASE_URL` when insights should go
elsewhere: a local model can write every meeting's summary while a hosted one
answers a question somebody asks by hand. The bounds are per step for the same
reason — a local model needs a longer timeout than a hosted API.

Which endpoint an insight will actually reach is reported by
`GET /operator/settings/llm` as `effective.insight`, with `inherited: true`
whenever it is not the insight step's own.

`CASSINI_SUMMARY_DISABLED` stops meetings being summarised while leaving the
endpoint configured. It means "publish meetings without a summary", so it does
not disable insights; leave the endpoint unset if the intent is that nothing
calls a model at all.

## What the endpoint sees

When a summary or an insight runs, the **full transcript text of the meetings
involved is sent to the configured endpoint**. If that endpoint is external, its
operator processes what it receives under its own terms; review them before
configuring it.

Call audio and the recording itself are never sent off your infrastructure for
either step. With no endpoint configured, nothing leaves: the local transcript is
still produced and published, the summary is skipped, and the app offers no way
to ask a question. There is no telemetry.

Anyone who is not an administrator can check this for themselves.
`GET /operator/setup` is readable by any logged-in Nextcloud account and carries
two bits — `features.summaries` and `features.insights` — and nothing else: no
endpoint, no model, no key. The full note is in
[Privacy and data processing](/docs/guides/privacy).

## Running an insight

An insight asks one question of several meetings and keeps the answer.

1. **Pick the meetings** in Browse. The selection is bounded to 20 meetings, and
   the app says how many to unpick if you go over.
2. **Open Prepare context**, and pick the Generate card.
3. **Choose a template**, and the provider and model that will answer. Choosing a
   template shows the question it puts to the meetings and the shape of the
   document that comes back. One shipped template, **Ask your own question**,
   takes a question you type: the answer in at most three sentences, the
   evidence with who said what, and what these meetings do not answer.

   Picking the provider is offered to everyone, not only administrators: which
   of the configured endpoints sees your transcripts is the asker's decision.
   The list carries each endpoint's name and nothing more. A person who is not
   an administrator sees no template picker, because the template registry is
   admin-only; their run uses the template the administrator configured, and the
   card says so.
4. **Press Generate.** The run appears at the top of the Browse list as
   `queued`, then `running`, then a result or a failure. Two runs are performed
   at a time; the rest wait, and say they are queued.
5. **The answer is written into your own Nextcloud Files**, under a **Cassini
   Insights** folder in your home. You own the file, so Nextcloud's own sharing
   works on it and nothing new decides who may read it. A document is never
   overwritten: each file is named for the day, the template and the run it came
   from, and records which meetings it read, which prompt and which model
   answered.

Every meeting an insight covers is fetched **as you**, over WebDAV, at the moment
the run happens. Nextcloud's own per-file permissions decide what is in the
bundle, so a run can only ever be assembled out of meetings you could already
open yourself. A retry re-checks that: a meeting you can no longer read stops the
run rather than quietly dropping out of the answer.

A failed run names the cause — no endpoint configured, the endpoint refused the
request, the model did not answer, the answer could not be written, or the
request itself — and carries **Retry**, from its card in the Browse list and
from the panel that started it. For an administrator it links to the AI
providers panel. Retry replays the request as you made it, to the endpoint you
picked; it falls back to the deployment's configured endpoint only if you picked
none, or the one you picked has since been removed.

Outside the app, `cassini insight run` asks the same question of a context
bundle — the document `cassini meetings context` prints. See
[Agent access via the CLI](/docs/guides/agent-access) for how that bundle is
assembled, and under whose permissions.

## Related

- [Privacy and data processing](/docs/guides/privacy) — what is sent, by whom,
  and what your provider sees.
- [Install on Nextcloud](/docs/getting-started/install) — the deploy variables
  that seed these settings on first start.
