# Development 

This document details the existing planning / development documentation and proposes a new and refined way of organising the documentation of development effort.

## Existing progress tracking

The progress development is specified and tracked in the `planning` directory.
The planning is centred around a strict hierarchy: project (implied) -> initiative -> task

---

### Taxonomy

IMPORTANT NOTE: this taxonomy is an old and stale one. The entities defined here are defined exactly as they were understood in the previous development phase, and shouldn't be confused with the desired taxonomy detailed below.

#### Project (implied)

The project is a repo / product -- in this case this would have been the Cassini (or GoCassini) project.
It's implied as it's not strictly mentioned and it doesn't affect the progress that much (it's a container of all effort put into the product).

#### Initiative

The initiative is considered a phase of project development, e.g. internal mvp (or `mvp` as it's called in the current `planning` directory).
The initiative is defined in terms of what success looks like and high level requirements for us to call this done.

The initiative itself is loosely shaped in terms what's in scope and what's out of scope.
The initiative has a list of tasks defined:
- ex-ante -- while shaping the initiative
- during development -- smaller requirements that surfaced during development, follow ups (for tasks that focused on smaller scope and exposed follow ups), reactive work (something's not working correctly, or not really up to desired quality)

The initiative is somewhat bound to time horizon (a sprint), with possibility (but one we try to avoid) of spilling over into a next cycle.

#### Task

A task is a ticket in a project management system (we're using Linear), and each task corresponds to a particular Linear ticket.
A task is a chunk of work a developer does, strictly defined requirements, includes a shaping process defining:
- what the requirements are for the task
- exploring unknowns (spiking), e.g. API surface, suggesting potential options, etc.
- shaping, breadboarding and slicing: defining WHAT we're implementing, HOW we're implementing and IN WHAT ORDER
- the task may also define follow ups (effort strictly out of scope for the task, but one that should be tackled in downstream tasks)

--- 

### Document shape

The current document shape follows a strict initiative -> task hierarchy.
The documents for a particular task are stored in `planning/initiatives/{initiative}/{task}`:
- initiative we've worked on is `mvp`
- the tasks are often mapped to Linear tickets in naming, e.g. `D-288-1-1-meeting`, where `D-288` is the linear ticket

The documents for each task may include (but are not limited to):
- `brief.md` - an unstructured brief detailing the task (may actually have structure, but that structure will vary from task to task)
- `framing.md` - optional framing document (as described by the shaping skills)
- `shaping.md` - the shaping document (as described by the shaping skills)
- `spike-{topic}.md` - a document detailing a particular spike (as described by the shaping skills)
- `breadboarding.md` - breadboarding definition (as described by the shaping skills)
- `slices.md` - implementation slices, following breadboarding (as described by the shaping skills)
- `implementation.md` - detailing the implementation (after execution) in terms of what exactly was done
- `followups.md` - potential followups stemming either from ex-ante planning out-of-scope items, or discovered during development
- ...other task related docs, e.g. explorations, comparisons, how-to-use documentation, etc.

---

### Planning / execution flow

The initiative is somewhat strictly defined:
- some (limited) time for exploration ex-ante
- general definition and constraionts - ex-ante
- additional tasks added (manually) during development

The initiative is planned ex-ante (at least the part of it), this includes:
- broad shaping
- identifying tasks
- splitting tasks into tickets and opening tickets on Linear
- additional tickets opened during development (based on information surfacing during development)

IMPORTANT FEATURE: this way of planning / execution includes a backlog, i.e. tickets being defined with inheritance:
- A -> B -> C - have a linear dependence, and all three are defined ex-ante

---

## Desired (proposed) progress tracking

Points of migration:
- move the `planning` into `development`
- separate what-was-done and what's-being-done
- redefine the taxonomy
- update the process and relationships within entities

### Taxonomy

IMPORTANT NOTE: this is the desired taxonomy and should be what we migrate to.

The ontology is more permissive (as opposed to previous hierarchical) and allows for many-to-many relationship. I've included the ontology section below.

The common theme in taxonomy (and ontology) is the basic definition, allowing for exploration (additional infromation surfacing) during the development / execution.

#### Initiative

The definition of initiative is Linear inspired (compatible), and in this case we would consider the initiative a Cassini MVP.

Unlike the strictly specified MVP, Cassini MVP in this sense, is a broad effort for which we don't have a clear idea on what exactly it will entail.
We're allowing the vision to crystalize as we're working on the initiative through projects and cycles.

The initiative CAN be mapped to time horizon, but isn't strictly tied to it by design.

#### Project

Project is a chunk of initiative-related work. An example of a project in these terms would be the Cassini MVP 1 (what was perviously considered an initiative).

Cassini MVP 1 included some requirements and a contained unit of work. The project is similar where it relates to a particular global feature with business definition, examples:
- Cassini MVP 1 (Cassini internal MVP) - we wanted to be able to use Cassini internally with feature set (functionality) we've defined as requirements
- Nextcloud marketplace readiness - a colletion of features we need in order to deploy to Cassini to Nextcloud market place as registered app
- Viewer access controls - employing fine-grained (configurable) access for the viewing of the meetings

#### Goal

Goal is a smaller scope of functionality than the project. The project is a particular effort that may have 1 or more goals related to it.

The example of a goal would be:
- the ability to record private meetings
- the ability to use Nextcloud-integrated storage (rather than our own managed storage)
- the modular architecture allowing for pluggable components: auth, storage (persistence), summarization

The goal can be tied to one or more projects, and it can be projectless, e.g. modular architecture allowing for pluggable components doesn't map cleanly to a particular business requiremnt, but DOES provide the groundwork for ease of development / maintainability.

#### Task

Tasks ARE under goal hierarchy. The main difference between the existing and desired treatment of tasks is the no-backlog pattern:
- a task is started as a chunk of development towards a particular goal
- a task is a contained effort
- a task may include some exploration and then implementation (along with decisions)
- a task may satisfy the goal completely or detail followups after implementation
- after a task that includes followups is implemented, the next task can be created to address those followups (or other gaps to goal completion)
- the tasks are created before Linear tickets and shold be enumerated based on the development tracking, but each task should be created as a linear ticket (and the link documented)

#### Phase

Unlike aforementioned entities, a phase isn't closely tied to particular effort, but rather serves as a real-world time interval, which may or may not be defined ex-ante.
The phase exists to represent a somewhat-contained collection of tasks / goals / projects executed over a time horizon.
As mentioned, a phase may or may not contain hard limits defined ex-ante:
- the time limit may be planned loosely and defined more cleary as we move through the phase
- the time limit may be defined strictly in advance (if such is the nature of the engagement, e.g. strict budget)

#### Cycle

Cycle is strictly defined time interval, e.g. 2 week cycle, a 4 week cycle, etc. (you can think of cycle as a sprint).
Cycle is under the hierarchically under a phase.
The cycle defines which projects / goals should be completed in the cycle, which one are nice-to-have, and which ones are strictly deferred.

---

### Ontology

The previous structure included a hierarchically related taxonomy, but the desired one has a more complex ontology which I'm detailing here.

#### TL;DR

The most important point is that the ontology isn't strictly hierarchical and, while most of the entities fall under loose hierarchy and **prefer many-to-one** relationship, we **allow for many-to-many** relationship in some cases.

The second-most important point is: we don't force the structure ex-ante. However, we do strongly perfer the majority of effort to be defined at the beginning -- if I had to pull some numbers out of my hat, I'd say: we prefer the 80% of the goals to be defined in the first 20% of the cycle.
Refering back to Shape Up, this is where uphill / downhill comes into play.
Finally, we employ no-backlog by implementing the next chunk which may satisfy a full "parent" entity (e.g. goal -> task), but in any case moves us closer to completion.

#### Project <-> Goal

Projects and goals have a **looesly** one to many relationship: 
- a project can have one or more goals
- a project is considered done after all the goals related to the project are completed
- a project may start with some goal definition, but the set of goals associated with the project might grow as the project is being worked on

Project should have a clear high-level definition of "what success looks like" - this isn't a goal, this is the golden state the project is looking to implement.
The goals are defined based on "what success looks like", but we allow for surfacing of additional goals as we move through the development. 

#### "Orphaned" goals and many projects -> one goal

We allow for a goal that isn't tied to a particular project, but is a nice-to-have sidecar. This will usually be code hygiene: architecture, CI, tests, e.g. modular architecture.

Furthermore, in some cases (although I expect this to rarely happen), a single goal might move two projects forward and, the completion of both projects might depend on that single goal.

#### Goal <-> Task

Just like Project <-> Goal, Goal <-> Task will **naturally** have a **one-to-many relationship**. Now, although a single task might move forward towards multiple goals, there's no strict dependence (e.g. "this goal is considered done after task X is in").

The more important relationship is this:
- multiple tasks might be associated with moving towards the goal
- the tasks might fall into multiple tracks, e.g.:
    - task T1 works towards goal G1 from one angle 
    - task T2 works towards goal G1 from another angle 
    - then, **only after T1 is done**, a T3 might pick up from where T3 left off and move towards the goal G1 from the same (or simimlar angle)

#### Task lineage and no-backlog

This is the important thing about tasks:
- two tasks might work towards a single goal from multiple tracks
- there **will** extist a task lineage (e.g. T1 left some followups, and T3 is building on those and moving forward), this lineage happens only ex-post
- there **is no forward looking lineage**, i.e. if T1 is being worked on, we don't have a T3 in-standby, blocked by T1

---

### Document shape

Documents are defined more strictly under this version of project tracking.

The directory structure:
- `development`:
    - `archive`
        - `PHASE-{n}`:
            - `{project}`
            - `{goal}`
            - `{task}`
    - `PHASE-{n}`:
        - `{project}`
        - `{goal}`
        - `{task}`

To elaborate:
- archive:
    - the archive contains an archive of wrapped up historic work
    - the work is archived based on (namespaced to) phases
    - under each phase, goals and tasks live side-by-side
    - a phase archive may include some additional docs, and those should be specified (I see `implementation.md` and `followups.md` being good candidates)
- current phase:
    - current phase is the sibling to archive and stores work-in-progress development docs
    - projects, goals and tasks live side-by-side

Each phase is enumerated sequentially as `PHASE-{n}`.
Each project is enumerated sequentially as `P{n}-{short name}`.
Each goal is enumerated sequentially as `G{n}-{short name}`.
Each task is persisted as the corresponding linear ticket: `D-{linear ticket number}-{short name}`

The full project and goal document spec is TBD.

Each task must contain the following documentation:
- `brief.md` - an unstructured brief detailing the task (may actually have structure, but that structure will vary from task to task)
- `framing.md` - framing document (as described by the shaping skills)
- `shaping.md` - the shaping document (as described by the shaping skills)
- `spike-x{n}-{topic}.md` - a document detailing a particular spike (as described by the shaping skills)
- `breadboarding.md` - breadboarding definition (as described by the shaping skills)
- `slices.md` - implementation slices, following breadboarding (as described by the shaping skills)
- `progress.md` - slice by slice progress updated live during the implementation
- `implementation.md` - detailing the implementation (after execution) in terms of what exactly was done
- `followups.md` - potential followups stemming either from ex-ante planning out-of-scope items, or discovered during development
- `tutorial.md` - a document created after implementation detailing how the user (or other reviewing participants) can manually test the code implemented in the task
- ...other task related docs, e.g. explorations, comparisons, 3rd-party how-to-use instructions, etc.

**note:** this list of per-task documens is a spec going forward (when migrating executed tasks, we don't force backfilling)

