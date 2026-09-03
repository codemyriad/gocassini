# Cassini agent skills

Skills an agent installs to work with Cassini meeting recordings: one that reads
them, and four that turn what it read into something — a summary, grounded
commitments, a shaping draft, or a retrospective.

A **skill** is a folder with a `SKILL.md` in it, in the format described by the
[Agent Skills specification](https://agentskills.io/specification). Roughly every
coding agent now reads them, and the same folder works across all of them.

```text
                       ┌──────────────────────────────┐
    Nextcloud ────────►│  cassini-meetings            │
    recordings         │  finds the meeting, reads it │
                       └───────────────┬──────────────┘
                                       │  cassini.meetings.context.v1
                                       │  ("a context bundle")
                ┌──────────────────┬───┴──────────────┬──────────────────┐
                ▼                  ▼                  ▼                  ▼
        cassini-meeting-   cassini-meeting-   cassini-meeting-   cassini-meeting-
            summary              todos             shaping              retro
                │                  │                  │                  │
                ▼                  ▼                  ▼                  ▼
          a summary          commitments       a shaping draft    a retrospective
```

Every skill in the second column consumes the same thing — a **context bundle**,
the document `cassini meetings context <id>` prints — so they compose: read once,
run any of them, run several.

## Install

```bash
# Everything, with an interactive picker for which agent to install into
npx skills add codemyriad/gocassini

# Just look at what is in here
npx skills add codemyriad/gocassini --list

# Claude Code, this project, no prompts
npx skills add codemyriad/gocassini -a claude-code -y

# One skill
npx skills add codemyriad/gocassini --skill cassini-meeting-summary -a claude-code

# Globally, for every project on this machine
npx skills add codemyriad/gocassini -g -a claude-code -y
```

**Pass `-a <agent>` when an agent runs the install for you.** Run from inside an
agent session the CLI is non-interactive and may install only to the universal
`.agents/skills/` directory, which Claude Code does not read.

Working inside a checkout of this repository, install from the working tree so
you get the skills as they are on your branch rather than as they are on `main`:

```bash
npx skills add ./ -a claude-code -y        # symlinks; edits to skills/ are live
npx skills add ./ -a claude-code -y --copy # independent copies (Windows)
```

That is safe to run here: the installer detects that the source and destination
overlap and skips rather than touching `skills/`.

Later: `npx skills update <name>`, `npx skills list`, `npx skills remove <name>`.

## The skills

| Skill | What it does |
|---|---|
| [`cassini-meetings`](./cassini-meetings/) | Lists rooms and meetings, pulls one meeting's transcript and summary as a context bundle, or downloads its portable `.opus`. Start here — the other four take its output. |
| [`cassini-meeting-summary`](./cassini-meeting-summary/) | A focused answer or fixed-shape summary grounded in the final state of one or more meetings. |
| [`cassini-meeting-todos`](./cassini-meeting-todos/) | Explicit commitments grouped by transcribed speaker, with unconfirmed assignments and unowned work kept separate. |
| [`cassini-meeting-shaping`](./cassini-meeting-shaping/) | A meeting-grounded shaping draft: problem, evidenced requirements, discussed shapes, a fit check and unresolved questions. |
| [`cassini-meeting-retro`](./cassini-meeting-retro/) | A retrospective participants held, or retrospective analysis explicitly derived from ordinary work meetings. |

## Prerequisites

The skills drive the `cassini` CLI, which talks to a Nextcloud instance running
the Cassini app. Nothing here works without it.

```bash
export CASSINI_NC_URL="https://cloud.example.com"
export CASSINI_NC_USER="<your nextcloud user>"
export CASSINI_NC_APP_PASSWORD="<Settings -> Security -> Create new app password>"
```

Use `./bin/cassini` from a Cassini checkout, or `cassini` if it is on `PATH`.
`cassini meetings context` also needs `ffprobe`. The full walkthrough, including
what each failure means, is
[`docs/agent-meeting-access.md`](../docs/agent-meeting-access.md).

**Access is Nextcloud's decision, not Cassini's.** Every request is made as a
Nextcloud user and sees exactly the recordings that account may read.

## Anatomy of a skill here

```text
skills/<name>/
├── SKILL.md            # the agent-facing procedure. Loaded when the skill triggers.
├── prompts/            # versioned single-shot contracts. Not read by the agent.
│   ├── <workflow>.v<version>.md
│   └── <workflow>-template.v<version>.md
└── references/         # loaded on demand, when SKILL.md names the trigger to load it
```

`SKILL.md` carries two frontmatter fields, `name` and `description`, and nothing
else — anything beyond the specification's six fields makes packaging fail on
some clients. `name` must equal the directory name.

### Why `prompts/` exists

Each artifact workflow has an agent procedure and a single-shot contract. Their
shared artifact semantics must not drift:

```text
   AUTHORING HOME                RESOLUTION                RUNTIME
   skills/<name>/prompts/  ──►   pinned copy, by      ──►  the product runs the
   <workflow>.v<version>.md      content hash              selected pinned bytes
                                                           which ones it ran
        │                                                         ▲
        └────────── the current eval design grades v0 only ───────┘
```

The prompt files are the authoring home for single-shot contracts. The product
resolves a selected version to a pinned, content-hashed copy — a vendored
dependency, not a prompt store — so an artifact can record exactly which bytes
produced it. Prompt versions are immutable: improve a contract by adding a new
versioned pair, then deliberately update its consumers and evals. **There is no
prompt editor in the product, and adding one is not a feature that is waiting to
be built.**

`cassini-meeting-summary/prompts/summarise.v0.md` and its template remain byte-
identical to what the transcription pipeline embeds today
(`cassini-go-recorder/internal/transcribe/templates/`). The tightened contracts
live in the four `v1` pairs; product adoption is a separate, explicit version
change, and the other three workflows still await the insight-run seam. Change a
workflow by adding a new prompt version alongside the old one, never by editing
only `SKILL.md` and hoping the two agree.

## Evals

The eval design — fixtures, what is checked deterministically, what needs a
judge, and how two models get compared honestly — is
[`docs/proposals/workflow-skill-evals.md`](../docs/proposals/workflow-skill-evals.md).
Its behavioural catalogue is explicitly pinned to the preserved `v0` contracts;
the `v1` pairs need versioned checks and golds before they can claim comparable
model results. No eval has been run yet; the proposal says so where it matters.

## Contributing a skill

- Directory name, and the `name` in frontmatter, are the same lowercase-hyphen
  string. Prefix it `cassini-`: on a consumer's machine every skill they have
  installed shares one flat namespace.
- `description` says what the skill does **and when to use it**, in the third
  person and under 1024 characters. Keep it concise and discriminating; name a
  sibling only when that boundary prevents likely cross-triggering.
- Keep `SKILL.md` under 500 lines and put anything longer in `references/`,
  naming in `SKILL.md` the condition under which to read it. A generic "see
  references/" does not get read.
- Front-load the rules that must not be lost: after a context compaction only the
  first part of a skill is retained.
- Add a `changelog.d/` fragment for anything user-facing, per
  [`CONTRIBUTING.md`](../CONTRIBUTING.md).

These files carry this repository's licence — see [`LICENSE`](../LICENSE).
Installing a skill copies its text into the installing repository, so whether
that licence is the right one for skills specifically is a decision worth making
deliberately rather than inheriting.
