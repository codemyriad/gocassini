# System overview

Cassini turns the file-driven CLI pipeline into a managed runtime.

## The stack in one view

```text
browser -> control panel -> operator API
operator -> cassini doctor / record / build / publish
operator -> SQLite + work root + shared published-site storage
viewer -> shared published-site storage (read only)
```

## The three services

### Operator

The operator is the control plane.

It owns:

- job admission
- job and attempt persistence
- stage transitions
- record/build/publish execution through CLI subprocesses
- live-site promotion
- SSE updates for the control panel

### Control panel

The control panel is the browser UI for operating the runtime.

It owns:

- job creation
- stop and rerun actions
- jobs and attempts inspection
- live operator state updates

### Viewer

The viewer is the final read-only surface.

It owns:

- serving the static meeting library
- meeting playback and transcript review

It does not talk to the operator.

## The pipeline inside the runtime

Cassini still follows the same durable artifact flow:

```text
Talk room
  -> .run bundle
  -> .meeting bundle
  -> .site bundle
  -> viewer site
```

In operator mode, those artifacts are managed under the operator's work root and shared site root.

## The boundaries that matter operationally

### The operator orchestrates, not reimplements

Stage execution still comes from the Cassini CLI:

- `cassini doctor --target record`
- `cassini record`
- `cassini build`
- `cassini publish`

### The control panel is an operator client

The control panel talks only to the operator API. It does not read SQLite or artifact files directly.

### The viewer is static and read-only

The viewer reads published files only. It does not create jobs, mutate state, or depend on the operator API at runtime.

## The storage picture

The operator keeps three distinct shapes of state:

- **SQLite** for job and attempt state
- **`current/`** for canonical reusable artifacts per job
- **`runs/`** for attempt-local retained artifacts and logs
- **live `published/` site** for what the viewer serves now

That split explains:

- why reruns can reuse preserved artifacts
- why failures remain inspectable
- why publish is serialized
- why the viewer can stay simple

## The runtime model in one sentence

The operator accepts a meeting request, preserves its execution history, reuses durable files between stages, and promotes a successful static site into a shared live root.

## Where to go next

- Compose and container shape: [Deployment stack](./deployment-stack.md)
- Job and attempt behavior: [Operator runtime](./operator-runtime.md)
- Filesystem and live-site swaps: [Storage and promotion](./storage-and-promotion.md)
