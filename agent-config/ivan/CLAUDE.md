## Principles

### Planning (shaping)

When planning we use shaping skill(s): framing-doc, shaping, breadboard reflection.
NEVER use shaping skills unless explicitly instructed to do so.

The shaping process goes like this: framing -> shaping -> (optionally) spiking -> breadboarding -> slicing -> execution

The shaping docs are stored in `development/<number>-<task>/`, example:
- `development/000-initial-setup`
- `development/001-observability-stack`

Each `developement` directory should contain the following documents:
- `framing.md` - framing document as specified by the framing skill
- `shaping.md` - this is the shaping document
- `spike-x{n}-{topic}.md` (optional) - a spike document for a particular spike (if needed)
- `breadboarding.md` - created when needed - breadboarding doc
- `slices.md` - created when needed - implementation slices
- `open_questions.md` - working open questions (more below)
- `implementation.md` - what was implemented (more below)
- `tutorial.md` - the tutorial on how to manually run the code (shell commands, script runs, manual interaction -- UI -- etc.)
- `followups.md` - aspects cut from that particular effort, or leads for further development

IMPORTANT: whenever I say planning dir / folder, shaping dir / folder, planning workspace, I mean the `development/<number>-<task>` directory.
Example:
```
==== user ====
Write up the challenges you had encountered, how I can reproduce and solve manually. Persist it in `challenges.md` in the planing dir.
==== agent ====
<creates `development/<number>-<task>/challenges.md`>
```

framing -> shaping -> spike(s) -> breadboarding -> slices are created during the shaping process one by one.

Spike(s) aren't necessary, but if needed, the `spike-x{n}-{topic}` documents a spike effort for a topic.
Spikes should be addressed one by one. 
Spikes will usually be exploration efforts (e.g. exploring a particular API, capabilities or unknown requirements, etc.), but could include a back and forth with me to define preferences more clearly (e.g. desired API, architecture, etc.).
When starting spikes, address them one by one, but explore as much as you can (on factual questions) and ask me only for preferences or additional information. It's ok if you can't find factual information, you can ask me in those cases as well.

Open questions are a working document used to prompt me for input.
When prompting with open questions, use the following format:

```
Q1:
- **Question:** <question>
- **Suggestion:** <sensible suggestion>
- **Rationale:** <rationale for the suggestion>
- **Alternatives:** <2-3 alternatives>

Q2:
- **Question:** <question 2>
...
```

I will respond to questions like so:
```
Q1: <response>
Q2: <response>
...
```
I might respond with OK, a custom message, or a question (in case we need to clarify a bit more)
If I omit a particular response (e.g. Q2), assume I'm giving you a confirmation for the suggestion for that question

Open questions should be used like so:
- note the open questions in a relevant document (e.g. `shaping.md`, `spike-x{n}-{topic}.md`)
- note the open questions in `open_questions.md` - always overwrite -- this file is ephemeral
- upon answering: persist responses in the relevant document
- if a question is not answered and thus considered confirmed, note: `affirmative (implied)`

`implementation.md`, `tutorial.md` and `followups.md` are created after the effort is done (slices implemented)

### Boiling the lake

When planning for scope, use the boil the lake analogy. In a nutshell it says:
- we used to say "don't boil the ocean", as in: take it one step at a time
- with agentic development the marginal cost of coding goes to zero
- we still can't "boil the ocean", but "boiling the lake" makes sense
- to put it explicitly: don't build an entire system at once, but when developing a feature, aim for completeness (in sensible boundaries)

This means: if we could squeeze in some related work, we could try:
- check Linear for open tickets related to this work and include it in the planning process
- we might add some and defer others, which is perfectly fine, but we make those decisions during the shaping process

### Executing the plan

When executing follow the shaped up plan, and implement slice by slice. 
Start the execution if I tell you to execute / implement / any other signal to start executing the plan (after planning), or if I start a session and tell you which task to execute.

The execution should flow like this:
- implement a slice
- validate and fix until validation passing
- commit
- move to the next slice

When committing, use the repo standard commit messages.

When told to execute / implement a plan, work slice by slice until done.
Stop only if you encounter error or a blocker that requires my attention / input.

During the execution: 
- create `progress.md` listing slices and their current state
- after each slice is committed mark it as done, the current one in progress, and the remainder as TODO
- use icons rather than words to mark the progress 

At the end of the execution crete `implementation.md` and `tutorial.md`

#### Tutorial

`tutorial.md` should be created as an instruction for the user to run functionality in the implemented task. 
This may include (but not limited to):
- UI interactions (if applicable)
- CLI / shell commands

If some setup is required prior, don't assume it's there, detail it in the document.

### Common workflow

I will ask you to start a new task and describe what the task is about and instruct you to plan it. 
In this context, this means:

1. Create a new task in `development/<number>-<task>/`, and persist the instruction in `brief.md`. When creating the task, look up Linear for the related ticket and other similar tickets (see boil the lake analogy above). The related linear tickets (regardless of being included in the final plan or deferred) should be included in `brief.md`
2. Perform the full shaping process (incl. framing), perform spikes if necessary, however many necessary. When shaping up, perform the full process and ask me only the questions that need my attention or require my preference.
3. Ask the questions (`open_questions.md`), if any
4. Stand by for execution or comment

At this point I might correct you, state a different preference, steer the planning process, or OK the plan and kick off the execution.

When the execution is confirmed, i.e. I instruct you to go forward with the execution:
1. commit the plan (see commit conventions below)
2. start the execution

During the execution work on slices one by one until done. After each slice is implemented and validated, update progress.md and commit the slice.
Work until done or until you encounter an insurmountable challenge and need my attention.

## Commit style

Example for planning commit `001-observability-stack`:
```
001-observability-stack: planning
```

Example for task `001-observability-stack`:

```
001-observability-stack: impl slice {n}:
* bullet 1
* bullet 2
```
