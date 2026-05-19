# Cassini microsite site map

A lightweight information architecture draft generated from the source-of-truth layer.

## 1. Home

**Purpose**
- explain what Cassini is
- give the short product story
- link to the main technical paths

**Primary source inputs**
- `docs/source-of-truth/README.md`
- `README.md`

## 2. How it works

**Purpose**
- explain the record -> build -> publish pipeline
- introduce `.run`, `.meeting`, `.site`, and portable `.opus`

**Primary source inputs**
- `docs/source-of-truth/core-flows-and-artifacts.md`
- `docs/source-of-truth/system-architecture.md`

## 3. Operator and control panel

**Purpose**
- explain the service-backed operating model
- show how jobs, attempts, reruns, and publish promotion work

**Primary source inputs**
- `docs/source-of-truth/operator.md`
- `docs/source-of-truth/control-panel.md`
- `docs/source-of-truth/deployment.md`

## 4. Viewer

**Purpose**
- explain how meetings are consumed after publish
- clarify the static-file contract and transcript/display model at a high level

**Primary source inputs**
- `docs/source-of-truth/viewer.md`

## 5. Deployment

**Purpose**
- explain the packaged topology and persistent storage model
- give a minimal quickstart for technical evaluators

**Primary source inputs**
- `docs/source-of-truth/deployment.md`
- `deployment/README.md`

## 6. Limits and current scope

**Purpose**
- set expectations on what is stable now vs still implementation-shaped

**Primary source inputs**
- `README.md`
- `docs/source-of-truth/*`

## Optional later pages

- artifact contracts
- portable meeting format
- transcript and timing model
- deployment runbook
