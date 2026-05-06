---
shaping: true
---

# Operator Control Panel — Frame

## Source

### Linear D-266

> **What**
>
> We ~~need~~ -> *(would really like to have)* a dashboard to:
>
> * observe the pipeline state live (jobs, runs)
> * manually trigger jobs, reruns, etc.
>
> **Priority**
>
> Dev facing - for local usage / inspection
>
> **Cupcake**
>
> Pipeline observability (without manual triggers)
>
> **Nice to have**
>
> Ready for production - to integrate with the Nextcloud deployment (but should be portable — simple UI app, usable inside and outside of Nextcloud — outside of Nextcloud probably a priority)
>
> **Shoot for the start**
>
> Implement additional debug interactions - simulate different failure modes and observe recovery / error reporting / reruns
>
> **Notes**
>
> Inspiration can be drawn from GitHub actions dashboard

_No comments or attachments on the issue yet._

### User notes (2026-05-06)

> The dashbaord should live in `<root>/cassini-control-panel`.
>
> The dashboard shouldn't access any storage, it should call to `cassini-operator` instead.
>
> The config should include `CASSINI_OPERATOR_URL` env variable.
>
> We will probably need to extend `cassini-operator` to provide live stage / state subscriptions, so include that in the plan.
>
> The hard requirement is to be able to see jobs and high-level stages (record, build, publish).
>
> A nice to have is to be able to see substages of each step as well (but that is the first to cut if scope is tight).
>
> The highest priority is the ability to:
> - start a job by inputing the meeting URL and clicking a button
> - see it play out
>
> Use `cassini-viewer` for conventions (Svelte app).
>
> Don't worry about styling, that will be handled later.

---

## Pre-work: Operator control panel scope landscape

Four scope candidates surfaced in the issue and follow-up notes:

| Option | What it does | Who benefits | Signal strength |
|--------|-------------|--------------|-----------------|
| **A. Observability-only dashboard** | Shows pipeline state live, but does not start jobs. | Developer/operator inspecting the pipeline. | Explicit in D-266 as the **Cupcake**. Lower priority than trigger + observe. |
| **B. Trigger + live observe loop** | Lets an operator paste a meeting URL, start a job, and watch record/build/publish progress. | Developer/operator running the MVP flow locally or in demos. | Strongest signal: explicit in D-266 **What**, reinforced in user notes as the **highest priority**. |
| **C. Production-ready portable control panel** | Keeps the UI portable and usable outside Nextcloud, with a path to deployment integration later. | Future pilot/self-host operators. | Present, but explicitly a **nice to have** rather than the first cut. |
| **D. Debug/failure simulation tooling** | Adds extra controls to simulate failures and inspect recovery/rerun behavior. | Developers hardening the pipeline. | Mentioned as **shoot for the stars**; clearly below the first cut. |

**Why B now:** the strongest repeated signal is not "just observe" and not "production polish first." It is the operator loop of **start a job, then see it play out**. Observability-only is explicitly demoted to cupcake scope. Production portability matters, but as a boundary on the solution rather than the first slice of work. Failure simulation is valuable, but clearly belongs after the basic trigger-and-watch loop works.

---

## Problem

- There is no browser control surface where an operator can paste a meeting URL, start a job, and avoid dropping to API calls, curl, or shell flow for the common case. (D-266; user notes)
- Current operator state is inspectable through persisted job APIs, but there is no dedicated dashboard for watching the pipeline move through high-level stages as work is happening. (D-266; implied by the current `cassini-operator` runtime)
- The desired boundary is a **thin control panel over `cassini-operator`**, not a UI that reads runtime storage directly or becomes a second execution system. (user notes)
- Scope pressure is real: the must-have is jobs plus high-level stages, while substages, production polish, and debug/failure simulation are all secondary and should be the first things cut. (D-266; user notes)

## Outcome

- An operator can open a browser UI, enter a meeting URL, start a job, and immediately see that job appear in the control panel. (user notes)
- The control panel shows current/recent jobs and at least the high-level pipeline stages — `record`, `build`, `publish` — in a way that lets the operator watch work unfold. (D-266; user notes)
- The dashboard lives in a dedicated `cassini-control-panel` app, follows `cassini-viewer` conventions, and talks only to `cassini-operator` through a configured `CASSINI_OPERATOR_URL`. (user notes)
- If scope permits, the same operator surface can later expose richer attempt/substage/failure information, but that detail is subordinate to the first trigger-and-watch loop. (D-266; user notes)

---

## Less about

- polished styling or design-system work in this ticket
- embedding the first version inside Nextcloud
- direct browser access to operator SQLite, work-root, or site storage
- failure-simulation and debug tooling in the first cut

## More about

- a thin operator console over the existing `cassini-operator` job model
- one fast operator workflow: paste meeting URL, start job, watch high-level stages move
- extending the operator API where needed for live state delivery instead of rebuilding pipeline logic in the UI
- keeping the first version dev-facing, portable, and easy to iterate on
