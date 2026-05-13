# Follow-ups

## 1. Raw session reruns

Current rerun support starts only from a canonical ready `.run` bundle.

Follow-up work:

- support reruns from captured raw session outputs, not only finalized `.run` bundles
- define validation rules for when captured raw streams are safe to reuse
- extend operator rerun admission and downstream build inputs to handle those raw-session sources honestly
- make reporting clear about which source boundary a rerun used

## 2. Retention policy for retained runs

Current behavior retains all attempt-local artifacts under `runs/` indefinitely.

Follow-up work:

- define a retention policy for `.run`, `.meeting`, and attempt-log artifacts
- decide what remains on fast local storage vs. what moves to colder storage
- define cleanup timing and safety rules so canonical `current/` artifacts remain intact
- add operational visibility for retained-artifact size and cleanup outcomes
