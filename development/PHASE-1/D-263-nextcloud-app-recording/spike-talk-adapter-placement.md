## D-263 Spike: Talk adapter placement

### Context

The D-263 planning work has now narrowed the architecture to:

- Talk keeps native recording UX
- Cassini implements the Talk recording-backend contract
- Talk `/store` receives Cassini's final meeting `.mkv`
- runtime execution identity is `baseURL + roomToken`

That leaves one structural decision before implementation:

- **Should the Talk-specific recording-backend adapter live inside `cassini-operator`, or beside it while delegating into the same runtime?**

### Goal

Choose the adapter placement that best fits the current codebase and D-263 scope.

Specifically:

- compare "inside `cassini-operator`" vs "separate service beside it"
- evaluate fit against current operator responsibilities
- decide which choice minimizes duplicated lifecycle code and integration risk

### Sources used

- Current repo:
  - `cassini-operator/README.md`
  - `cassini-operator/internal/operator/run.go`
  - `cassini-operator/internal/operator/record_runtime.go`
  - previous D-263 spikes in this directory

### Outcome

This spike is complete enough to lock the structural choice for D-263.

Selected conclusion for D-263:

- **The Talk-specific adapter should live inside `cassini-operator`**
- **It should be implemented as an additional HTTP surface in the same process, not as a separate sibling service**
- **The generic `/jobs` API should remain available, but it should not be Talk's public integration seam**

Put more concretely:

- `cassini-operator` already owns the exact runtime concerns Talk needs:
  - process start/stop
  - worker-slot admission
  - persisted lifecycle state
  - job-to-attempt history
  - control over final output paths
- the missing piece is protocol adaptation, not a second orchestration system

## What `cassini-operator` already is

Per the local README and runtime code, `cassini-operator` is already:

- a long-running HTTP server
- an in-process scheduler/runtime
- the persistence layer for record/build/publish jobs
- the owner of stop semantics and in-memory live process registry

It is intentionally:

- orchestration
- persistence
- admission control
- observability

and intentionally **not**:

- a second implementation of recording/build/publish internals

That existing boundary is already close to what the Talk backend needs.

## What Talk needs from the adapter

The Talk recording backend contract needs an HTTP-facing component that can:

- accept signed `start` / `stop` requests keyed by room token
- map room token -> runtime execution target
- keep per-room runtime state
- issue `started` / `stopped` / `failed` callbacks
- upload the final `.mkv` to Talk `/store`

Those are not independent from operator concerns.
They sit directly on top of:

- process lifecycle
- stop coordination
- output-path knowledge
- persisted current-attempt state

## Option A: adapter inside `cassini-operator` — selected

Mechanism:

- extend the existing operator HTTP server with Talk-specific routes
- add Talk auth verification and callback/upload helpers in the same process
- let the Talk handlers use internal runtime primitives directly

Examples of what would live together:

- generic `/jobs` API
- Talk `/api/v1/room/{token}` start/stop API
- optional `/healthz` or equivalent readiness endpoint
- room-token -> running job mapping
- callback/upload logic using the same store/runtime state

### Pros

1. **One runtime truth**
   - The same process that starts a recording also knows whether it is running, stopping, failed, or finalized.

2. **No duplicated lifecycle control**
   - A separate adapter would otherwise need to proxy or mirror stop state, output paths, and retry/error handling.

3. **Direct access to operator-owned stop/process registry**
   - The operator already tracks live recording subprocesses in memory.
   - Talk stop semantics fit naturally there.

4. **Direct access to persisted attempt metadata**
   - The operator already knows the active job, attempt number, stage, artifact paths, and completion/failure state.

5. **Smaller D-263 implementation surface**
   - Add routes and runtime glue once, rather than inventing inter-service protocol between a façade and the operator.

### Cons

1. **`cassini-operator` becomes responsible for two HTTP surfaces**
   - generic operator API
   - Talk backend API

2. **The binary becomes slightly less "generic operator only"**
   - but this is still coherent because the Talk adapter is just another admission/control surface over the same runtime.

### Assessment

These downsides are acceptable for D-263 because:

- the repo already treats Nextcloud Talk as the primary live recording provider
- the operator is already the long-running networked runtime
- the new responsibility is protocol adaptation, not unrelated product scope

## Option B: separate Talk adapter beside `cassini-operator`

Mechanism:

- build a second process or binary that speaks Talk protocol
- that process then calls the generic `/jobs` operator API or some new internal operator API
- it also watches job state and performs callbacks/upload on completion

### Pros

1. **Cleaner conceptual separation on paper**
   - operator remains "generic"
   - Talk adapter remains "provider-specific"

2. **Potential future reuse if many providers need radically different protocol façades**

### Cons

1. **Two long-running processes instead of one**
   - more deployment, health, and configuration surface

2. **Requires a second internal protocol anyway**
   - the façade still needs a reliable way to:
     - start by room token
     - stop by room token
     - learn when recording is actually live
     - learn when final `.mkv` is ready
     - find the path to upload

3. **Split lifecycle truth**
   - one process knows Talk room-token state
   - one process knows real subprocess/job state
   - synchronization bugs become much easier to introduce

4. **Awkward stop semantics**
   - Talk stop is room-token based
   - current operator stop is job-id based
   - a separate adapter must keep its own token<->job mapping and reconcile retries/failures

5. **More moving parts for no real product gain in D-263**

### Assessment

This option only becomes attractive if the team explicitly wants:

- a provider-agnostic operator core consumed by many external façades
- separate deployability of provider adapters

That is not what the current codebase or D-263 scope is optimizing for.

## Why "inside operator" is the better fit

The current repo already points toward one-process ownership:

- the operator HTTP server already exists
- the operator already shells out to the Cassini CLI
- the operator already persists and classifies record/build/publish outcomes
- the operator already owns live stop coordination

The Talk adapter mostly needs to translate:

- Talk protocol
- into operator runtime actions
- and operator outcomes
- back into Talk callbacks/uploads

That translation belongs closest to the runtime it is translating.

## Recommended internal boundary

The recommended shape is not "smash Talk details everywhere into the operator".

It is:

- keep one binary/process: `cassini-operator`
- add a Talk-specific HTTP/controller layer inside it
- keep clear internal separation between:
  - generic runtime/store logic
  - Talk protocol adapter logic

Suggested internal split:

- **generic runtime layer**
  - worker slots
  - job/attempt persistence
  - record/build/publish subprocess execution
  - stop registry
- **Talk adapter layer**
  - request signature validation
  - room-token mapping
  - callback posting to Talk
  - `/store` upload
  - per-room adapter state

So the decision is:

- **same process**
- **separate package/module boundaries inside that process**

not:

- one giant undifferentiated operator file

## What this means for D-263 implementation

### I1

`I1` should establish the Talk-facing auth/readiness support inside `cassini-operator`.

### I2

`I2` should add the Talk room start/stop routes and their mapping into operator runtime actions.

### I3

`I3` should add Talk callback and `/store` upload logic, again inside `cassini-operator`, using the operator's own knowledge of completion state and output paths.

## Decisions locked by this spike

1. **The Talk adapter lives inside `cassini-operator`.**
2. **D-263 should keep one long-running process, not introduce a second Talk façade service.**
3. **The generic `/jobs` API remains useful, but Talk should integrate through a Talk-specific HTTP surface.**
4. **Implementation should keep internal separation between generic runtime logic and Talk protocol adapter logic, even though they live in the same binary.**

## Remaining planning status

At this point the major D-263 shaping ambiguities are resolved enough to move into implementation without another planning spike.
