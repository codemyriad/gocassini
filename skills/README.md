# Cassini agent skills

Skills an agent installs to work with Cassini meeting recordings: one that reads
them, and four that turn what it read into something — a summary, a per-person
to-do list, a shaping draft, a retrospective.

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
          a summary        to-dos per person   a shaping draft    a retrospective
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
| [`cassini-meeting-summary`](./cassini-meeting-summary/) | The meeting as a fixed-shape summary: overview, key points, decisions, action items, open questions, next step. |
| [`cassini-meeting-todos`](./cassini-meeting-todos/) | A to-do list grouped by participant, every item traced to the moment it was taken on, with what nobody claimed kept separately. |
| [`cassini-meeting-shaping`](./cassini-meeting-shaping/) | A shaping draft from a recorded design discussion: problem, numbered requirements with evidence, the shapes the room floated, a fit check, and what it left open. |
| [`cassini-meeting-retro`](./cassini-meeting-retro/) | A retrospective, from one recorded retro or from a run of meetings across a period. |

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
├── prompts/            # the single-shot bytes the product runs. Not read by the agent.
│   ├── <workflow>.v0.md
│   └── <workflow>-template.v0.md
└── references/         # loaded on demand, when SKILL.md names the trigger to load it
```

`SKILL.md` carries two frontmatter fields, `name` and `description`, and nothing
else — anything beyond the specification's six fields makes packaging fail on
some clients. `name` must equal the directory name.

### Why `prompts/` exists

Each of the four workflows runs in two places, and they must not drift:

```text
   AUTHORING HOME                RESOLUTION                RUNTIME
   skills/<name>/prompts/  ──►   pinned copy, by      ──►  the product runs the
   <workflow>.v0.md              content hash              pinned bytes and records
                                                           which ones it ran
        │                                                         ▲
        └──────────────── the evals grade these ──────────────────┘
```

The prompt files are the authoring home. A workflow is authored, versioned and
evaluated here; the product resolves it to a pinned, content-hashed copy — a
vendored dependency, not a prompt store — so an artifact can record exactly which
bytes produced it. **There is no prompt editor in the product, and adding one is
not a feature that is waiting to be built.**

`cassini-meeting-summary/prompts/summarise.v0.md` and its template are byte-
identical to what the transcription pipeline embeds today
(`cassini-go-recorder/internal/transcribe/templates/`); the other three await the
insight-run seam. Improve a workflow by editing the prompt file and cutting a new
version — `v1` alongside `v0` — never by editing prose in `SKILL.md` and hoping
the two agree.

## Evals

The eval design — fixtures, what is checked deterministically, what needs a
judge, and how two models get compared honestly — is
[`docs/proposals/workflow-skill-evals.md`](../docs/proposals/workflow-skill-evals.md).
No eval has been run yet; the proposal says so where it matters.

## Contributing a skill

- Directory name, and the `name` in frontmatter, are the same lowercase-hyphen
  string. Prefix it `cassini-`: on a consumer's machine every skill they have
  installed shares one flat namespace.
- `description` says what it does **and when to use it**, in the third person,
  under 1024 characters, and names the sibling skill to use instead when the
  request is really one of theirs. All five of these match on "meeting" — without
  that clause they cross-trigger.
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
