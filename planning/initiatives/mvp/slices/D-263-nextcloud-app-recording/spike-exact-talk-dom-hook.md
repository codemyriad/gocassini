## D-263 Spike: exact Talk DOM hook

### Context

The higher-level D-263 shape is already selected:

- standard Nextcloud PHP app
- logged-in JS shim for the Talk-facing trigger surface
- Nextcloud app route calls `cassini-operator`

The Talk UI spike already established that there is no documented public Talk plugin API for adding a custom in-call button.

That leaves one implementation-detail spike:

- **what exact DOM/context hook should the JS shim use in the current Talk frontend**

This matters because D-263 needs one least-brittle anchor, not a vague “find somewhere in the page and insert a button” strategy.

### Goal

Identify the current least-brittle hook for a Cassini “start recording” affordance in Nextcloud Talk, based on the current `nextcloud/spreed` frontend structure.

### Outcome

This spike is complete enough to lock the first-cut D-263 Talk hook.

Selected for D-263:

- **Conversation context source:** read the current Talk room token from the URL path, matching the current router path `/call/:token`
- **Page-presence gate:** only activate when the Talk main app is mounted under `#content`
- **In-call gate:** require the in-call top bar structure rendered by `MainView` / `TopBar`
- **Primary insertion target:** append a dedicated Cassini slot into `.main-view > .top-bar.top-bar--in-call .top-bar__wrapper[data-theme-dark]`
- **Placement rule:** insert the slot immediately before the existing conversation-actions menu when that menu host can be found; otherwise append as the last child of the wrapper
- **Reactive mechanism:** use a `MutationObserver` on `#content` plus patched `history.pushState` / `history.replaceState` and `popstate`
- **Explicit non-goal:** do not target generated internals of `NcActions` or translated menu labels as the primary anchor

## Evidence in current Talk frontend

### 1. Current route shape exposes the token in the path

Current router:

- `src/router/router.ts`

Current main route:

- `/call/:token`

The app also exposes a composable:

- `src/composables/useGetToken.ts`

That composable reads the route param `token`.

Conclusion:

- the least ambiguous external context source for our injected script is the browser URL path
- D-263 does not need to scrape the token from DOM text

### 2. The main app is mounted under `#content`

Current app bootstrap:

- `src/main.js`

It mounts the Vue app with:

- `.mount('#content')`

Conclusion:

- `#content` is the correct root observation boundary for the JS shim
- we do not need to observe the whole document

### 3. The meeting shell is `MainView` with `TopBar`

Current main conversation view:

- `src/views/MainView.vue`

Relevant structure:

- `.main-view`
- `<TopBar :isInCall="isInCall" />`
- `<CallView v-if="isInCall" :token="token" />`
- `<ChatView v-else />`

Conclusion:

- `.main-view` is the current page-level anchor for a conversation
- `TopBar` is always present in the conversation shell
- in-call state is expressed through `TopBar` props and classes

### 4. `TopBar` gives us a better anchor than `TopBarMenu`

Current top bar:

- `src/components/TopBar/TopBar.vue`

Relevant structure:

- root class: `.top-bar`
- in-call modifier: `.top-bar--in-call`
- current wrapper: `.top-bar__wrapper`
- in-call wrapper marker: `:data-theme-dark="isInCall ? true : undefined"`

Relevant child order in call mode:

- conversation header
- call time
- participants button
- optional dialogs
- `TopBarMenu`

Conclusion:

- the stable Talk-owned DOM contract we can see is the `TopBar` wrapper and its classes
- that is safer than trying to bind to internals of `NcActions`, which belong to a component abstraction rather than Talk’s own CSS/API surface

### 5. `TopBarMenu` is real, but its root DOM is less stable than the `TopBar` wrapper

Current menu component:

- `src/components/TopBar/TopBarMenu.vue`

It renders:

- `<NcActions forceMenu ...>`

This tells us:

- there is a right-side conversation-actions menu in call mode
- the menu exists conceptually

But it does **not** give us a stable raw DOM class from Talk itself for the outermost rendered node, because that comes from `NcActions`.

Conclusion:

- use the `TopBar` wrapper as the primary DOM anchor
- treat locating the existing menu button as a best-effort placement refinement, not the core hook

## Selected hook

### D263-DOM1: route/context gate

The shim should activate only when:

- `window.location.pathname` matches the current Talk conversation route
- recommended pattern: `/\/call\/([^/]+)(?:\/recording)?$/`

Selected token source:

- parse the token from the path

Why:

- this matches the current Talk router
- it is simpler and less brittle than scraping Vue state indirectly

### D263-DOM2: top-bar presence gate

The shim should then require the current Talk meeting shell:

- `#content .main-view`
- `#content .main-view > .top-bar`

If those are not present:

- do nothing

Why:

- this ensures we are in the Talk main app and not another page that happens to share global Nextcloud chrome

### D263-DOM3: in-call insertion target

Selected first-cut insertion target:

- `#content .main-view > .top-bar.top-bar--in-call .top-bar__wrapper[data-theme-dark]`

This is the current least-brittle in-call anchor because:

- it is Talk-owned markup, not a third-party component internals class
- it only appears in the in-call branch of `TopBar`
- it naturally groups the right-side call actions

### D263-DOM4: exact placement rule

Inside the selected wrapper:

1. try to find the existing right-side conversation-actions/menu host
2. if found, insert the Cassini slot immediately before it
3. if not found, append the Cassini slot as the final child of the wrapper

Selected Cassini mount container:

- `<div class="cassini-talk-action-slot" data-cassini-talk-slot="recording"></div>`

Why this rule:

- “before menu” gives the intended visual grouping when the menu host is available
- plain append keeps the hook resilient if the menu’s internal DOM changes

## What not to hook to

### 1. Do not anchor on translated text

Avoid selectors based on:

- “Conversation actions”
- “Start recording”
- any localized button title or label

Why:

- localization makes this brittle immediately

### 2. Do not anchor on `NcActions` implementation classes as the primary contract

Avoid making the first-cut hook depend on internal classes emitted by `@nextcloud/vue` action components.

Why:

- those are library implementation details, not Talk-owned view structure

### 3. Do not scrape conversation title or participant text for state

The room token and in-call state already have stronger signals:

- route path
- `.top-bar--in-call`
- `.top-bar__wrapper[data-theme-dark]`

## Reactive strategy

Selected D-263 shim behavior:

1. patch `history.pushState`
2. patch `history.replaceState`
3. listen to `popstate`
4. observe `#content` with a `MutationObserver`
5. run one idempotent `mountOrUpdateCassiniTalkAction()` on each of those signals

Why this is the right cut:

- Talk is a Vue SPA using history routing
- route changes may not trigger full page reloads
- call state and top-bar DOM can change after the route is already stable

### Idempotence rule

The shim should:

- reuse the existing `.cassini-talk-action-slot` when present
- remove it when route/context gates no longer match
- never create more than one slot per active meeting view

## Fallback path

If the exact in-call wrapper selector proves broken in a target Talk version, the fallback should be:

- `#content .main-view > .top-bar .top-bar__wrapper`

If even that is not workable, fall back to the already selected broader product fallback:

- a meeting-adjacent trigger surface instead of an in-call placement

## Why this is the right first cut

This hook balances correctness and pragmatism:

- it uses route structure explicitly owned by Talk
- it uses Talk-owned top-bar classes instead of deeper library internals
- it keeps the placement near the native call actions
- it stays isolated to one mount slot and one observer loop

## Concrete implementation implications

1. The JS shim should parse token from `window.location.pathname`.
2. The shim should gate on `.top-bar--in-call`.
3. The shim should mount into `.top-bar__wrapper[data-theme-dark]`.
4. The shim should render its own button markup and event handling inside `.cassini-talk-action-slot`.
5. The shim should call the Nextcloud app route, not Talk internals.

## Acceptance

This spike is complete because it resolves the last frontend hook ambiguity:

- exact route/context source: URL path `/call/:token`
- exact DOM root: `#content .main-view > .top-bar`
- exact in-call anchor: `.top-bar.top-bar--in-call .top-bar__wrapper[data-theme-dark]`
- exact placement rule: before the menu host when available, otherwise append inside the wrapper
- exact reactivity model: history hooks + `popstate` + `MutationObserver`

## Reassessment

This does not make the hook “supported” by a formal Talk extension API.
It does make it concrete, isolated, and testable.

The next work is no longer spike-level uncertainty.
It is implementation planning or a prototype of the shim against a target Talk version.
