# Recording-first processing on shared Talk hosts

Choose `CASSINI_PROCESSING_POLICY` (or `--processing-policy`):

| Policy | Behavior |
| --- | --- |
| `concurrent` (default) | Preserve existing overlap between recording and background work. No shared finalization lock. |
| `recording-first` | Defer builds until recordings finish; yield a running build when a new capture arrives; serialize recorder remuxes. Intended for small shared hosts. |
| `auto` | Allow overlap when measured host **and cgroup** CPU and memory have headroom. Use a smaller host CPU budget for GPU inference, retaining the existing VRAM admission check. |

`CASSINI_RECORDING_PRIORITY=true` / `--recording-priority` is a compatibility
alias for `recording-first`. Conflicting explicit policies are rejected.

Auto samples CPU usage every second, requires two sufficient samples before
starting during capture, and yields a running build after three insufficient
samples while capture is active. A new recording can overlap a running build
only with a fresh, sufficient observation. CPU headroom is the smaller of host
idle cores and the container quota minus current container usage, so a large
host with a small container is still treated as constrained. Unknown/stale
telemetry prevents overlap; no-recording periods can drain the queue normally.
Linux cgroup v2 telemetry is required for automatic overlap.

`CASSINI_PROCESSING_CPU_RESERVE=1` reserves a free core for live services;
`CASSINI_PROCESSING_MEM_RESERVE_MB=512` reserves free RAM while a build is running.
At launch, auto also budgets the CPU build's configured inference threads (one
host thread for CUDA), and the model-specific memory admission floor. It does
not charge a running build for its already-allocated CPU/model a second time.
GPU builds still need host CPU for decoding and host RAM. This policy monitors
headroom; it is not a hard CPU reservation or a guarantee against brief spikes.
Auto and concurrent retain parallel recorder finalization; only recording-first
adds the shared remux lock. The demo explicitly uses recording-first.

- `CASSINI_MAX_RECORD_WORKERS` bounds admitted recorder processes, including
  finalization. Raise this only after measuring the target's media capacity.
- `CASSINI_MAX_BUILD_WORKERS=1` is sufficient: builds remain serialized even if
  more queue consumers are configured.
- `CASSINI_RECORDING_IDLE_GRACE=5s` (or `--recording-idle-grace`) requires a quiet
  interval after the last recorder exits. Negative/invalid values are rejected.
The following describes `recording-first`:

- Builds remain durably `build/queued` while recording. Seal and publication
  workers also wait before starting a new job; an already-running seal/upload
  is allowed to finish.
- A new admitted recording cancels a running build, including its ffmpeg child
  process group. After the child exits, only the interrupted attempt's derived
  `.meeting` is discarded. The captured `.run`, original queue timestamp and
  attempt number survive. This yield does not consume resource-failure retries.
  A later idle period restarts transcription from the beginning; it does not
  checkpoint ASR. Continuous recording can intentionally defer results forever.
- Recorder processes flush/close capture before competing for a shared
  finalization file lock under the operator's work directory. One recorder
  remuxes at a time. Waits emit progress logs, time out after 20 minutes, and do
  not delete capture artifacts on failure. The lock is released on process
  exit/crash. The existing overall recorder-stop timeout still applies.

`GET /operator/status` exposes `scheduling`: policy, quiet interval, configured
workers and active recorder count. Waiting on recording priority is distinct
from a build blocked by unavailable compute or exhausted memory.

## Memory admission

Cgroup v2 headroom credits clean inactive file cache, excluding dirty/writeback
and shared pages conservatively. The estimate never exceeds the cgroup limit or
host `MemAvailable`. Missing cache counters fall back to limit minus current
usage. Model-specific floors and memory/CPU limits remain in force; this is not
an override of OOM protection or a periodic global cache drop.

## Scope and limitations

This targets capture/background contention and the demo's cache-accounting stall.
It does not eliminate Nextcloud's per-request Basic-auth cost or reserve CPU for
Talk. Shared-host CPU saturation can still degrade media before a build starts.
Finalization is serialized, not moved into a separate durable pipeline stage;
a recording slot remains occupied until finalization exits. Monitor disk space
and queue age: delayed builds retain raw captures longer. In-progress builds
release memory when cancelled; seals/uploads may briefly overlap a new recording.

Validate simultaneous and staggered stops, new captures during active builds,
artifact integrity, transcripts, publication, idle drain and restart recovery.
Do not treat passing short trials as sustained capacity.
