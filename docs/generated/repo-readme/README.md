# Cassini

Cassini records Nextcloud Talk meetings and turns them into durable meeting artifacts that can be reviewed in a browser or packaged as one portable `.opus` file.

## Primary flow

```text
Talk room
  -> cassini record
  -> cassini build
  -> cassini publish
  -> cassini serve / viewer
```

The same architecture also supports the simpler product-facing story of one portable file per meeting.

## Repo entrypoint

```bash
./bin/cassini
```

## First commands

```bash
./bin/cassini --help
./bin/cassini doctor
```

## Main components

- `cassini-go-recorder/` — live capture, build pipeline, portable packing, CLI implementation
- `cassini-publisher/` — static-site export
- `cassini-operator/` — orchestration, persistence, reruns, publish promotion
- `cassini-control-panel/` — operator UI
- `cassini-viewer/` — read-only browser playback and transcript UI
- `deployment/` — packaged Compose topology
- `harness/` — local stack and smoke flows

## Docs map

- developer docs: [`docs/generated/developer/README.md`](../developer/README.md)
- admin docs: [`docs/generated/admin/README.md`](../admin/README.md)
- microsite draft: [`docs/generated/microsite/README.md`](../microsite/README.md)
- system overview: [`docs/architecture.md`](../../architecture.md)
- deployment bundle: [`deployment/README.md`](../../../deployment/README.md)
