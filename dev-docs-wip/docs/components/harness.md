# Harness

The harness is Cassini’s local Talk lab.

Its job is to make end-to-end development reproducible by giving you a local environment for:

- starting Nextcloud Talk
- creating rooms quickly
- running smoke tests
- generating and streaming fixture meetings

If the operator stack is the product-shaped runtime, the harness is the lab it records against.

## What it is for

Use the harness when you want to:

- exercise the recorder against a real local Talk room
- create local demo rooms quickly
- run fixture or showcase flows
- run repeatable E2E and smoke tests

Do not think of the harness as part of the deployed Cassini product. It is a developer and test environment.

## Preferred entry points

From the repo root, use the `cassini dev` wrapper rather than calling harness scripts directly.

Main commands:

```bash
./bin/cassini dev stack up
./bin/cassini dev stack down
./bin/cassini dev stack status
./bin/cassini dev room create --name "Local room"
./bin/cassini dev smoke
```

Useful fixture and player commands:

```bash
./bin/cassini dev fixture prepare-showcase
./bin/cassini dev player showcase --call-url "$CALL_URL"
./bin/cassini dev player video --call-url "$CALL_URL" --duration 20
```

## Quick local flow

Start the stack:

```bash
./bin/cassini dev stack up
```

Create a room:

```bash
CALL_URL="$(./bin/cassini dev room create --name "Local smoke room" | tail -n1)"
echo "$CALL_URL"
```

Then either:

- open the URL in the browser yourself
- stream a fixture into it with a harness player command
- hand the URL to the Cassini operator in the control panel

## Local credentials and address

The local Nextcloud/Talk UI is served at:

- `http://127.0.0.1:28080/`

Default admin credentials:

- username: `admin`
- password: `admin`

## What lives under `harness/`

Important parts include:

- `compose.yml` — local Nextcloud/Talk stack
- `bin/` — stack lifecycle, room creation, smoke, and fixture scripts
- `scenarios/` — scenario inputs for generated meetings
- `media/` — generated media inputs and processed outputs
- `runtime/` — transient runtime state such as last room URL and logs

## Showcase and smoke flows

Two especially useful entry points:

### One-command smoke

```bash
./bin/cassini dev smoke
```

### Showcase meeting

```bash
./bin/cassini dev fixture prepare-showcase
CALL_URL="$(./bin/cassini dev room create --name "Lantern Festival Demo" | tail -n1)"
./bin/cassini dev player showcase --call-url "$CALL_URL"
```

Use the showcase flow when you want a more realistic meeting sample than a minimal smoke run.

## When to go deeper

If you are changing:

- local Talk stack behavior
- room creation/bootstrap behavior
- synthetic fixtures or streaming players
- CI E2E scenarios

then read the repo-level harness docs next:

- `harness/README.md`

## See also

- [Quick start](../quick-start.md)
- [Running the local developer stack](../local-developer-stack.md)
