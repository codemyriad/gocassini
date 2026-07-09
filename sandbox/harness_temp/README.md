# Sandbox harness snapshot

This directory is a sandbox-owned snapshot of the harness files that
`sandbox/deploy.sh` depends on. The files are copied from `main` at `d770d72`
(the pre-branch merge-base for `feat/harness-remote-support`) so harness
refactors do not change sandbox behavior accidentally.

Update this directory only when intentionally changing sandbox behavior.
