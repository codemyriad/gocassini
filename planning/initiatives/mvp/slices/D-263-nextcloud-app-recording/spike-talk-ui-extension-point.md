## D-263 Spike: Talk UI extension point

### Context

The D-263 shaping is already selected around this direction:

- standard Nextcloud PHP app
- external Cassini deployment
- signed app-to-Cassini trigger request
- Cassini continues to own the live recording flow

That leaves one narrow but important design seam:

- **where the moderator-facing "start recording" action lives in the Nextcloud Talk UI**

This matters because the selected D-263 shape explicitly wants:

- Nextcloud to be the operator surface
- the moderator to start recording from meeting context
- the risky UI surface to be isolated rather than spread across the whole integration

### Goal

Determine the narrowest viable UI insertion point for a Cassini recording trigger in Talk, based on current documented Nextcloud app surfaces and the currently visible Talk ecosystem shape.

### Outcome

This spike is complete enough to lock the first D-263 UI direction.

Selected for D-263:

- **Primary app shape:** standard PHP app
- **Primary UI loading mechanism:** load a logged-in script through Nextcloud app bootstrapping / additional-scripts events
- **Primary trigger strategy:** detect Talk meeting context client-side and render a small Cassini trigger affordance from our own script
- **Server boundary:** the frontend calls a Nextcloud app route; only the server-side route calls the external Cassini service
- **Explicit non-goal:** do not try to extend Talk through a documented public "custom in-call button" API, because the current public docs do not expose one
- **Fallback posture:** if the DOM hook proves too brittle in a target Nextcloud/Talk version, degrade to a meeting-adjacent trigger surface rather than reshaping Cassini into Talk's recording-backend protocol for D-263

## Investigated surfaces

### 1. Public Talk integration API

Official docs:

- https://docs.nextcloud.com/server/stable/developer_manual/digging_deeper/talk.html

What it gives us:

- server-side Talk broker availability checks
- conversation creation / deletion
- backend-oriented Talk integration

What it does **not** document:

- a public frontend extension point for injecting a custom meeting action into the in-call Talk UI
- a public plugin registry for the Talk top bar or call action menus

Conclusion:

- useful for backend awareness
- not the answer for a moderator-facing custom in-call button

### 2. Standard app bootstrapping and script loading

Official docs:

- https://docs.nextcloud.com/server/latest/developer_manual/app_development/init.html
- https://docs.nextcloud.com/server/latest/developer_manual/basics/front-end/js.html

What it gives us:

- logged-in additional-script loading during page render
- init-script and normal-script injection from the app
- a documented way to place app JS on logged-in Nextcloud pages

What it does **not** give us:

- Talk-specific semantic mounting points
- a guarantee that a particular internal Talk DOM/component path is stable

Conclusion:

- this is the documented way to get code onto the relevant page
- any Talk-specific action insertion done from that code is tactical, not a documented Talk extension contract

### 3. Generic "extend core parts" frontend plugin model

Official docs:

- https://docs.nextcloud.com/server/latest/developer_manual/basics/front-end/js.html

What it shows:

- there are explicit extension points for some core surfaces, for example the Files "new" menu

What it implies:

- when Nextcloud intends a frontend surface to be extensible, it documents a specific plugin registration model

What is missing for Talk:

- no comparable documented plugin registration point for Talk meeting actions

Conclusion:

- this is evidence against assuming a supported Talk plugin API exists for the in-call action area

### 4. ExApp / AppAPI UI surfaces

Official docs:

- https://docs.nextcloud.com/server/stable/developer_manual/exapp_development/tech_details/api/topmenu.html
- https://docs.nextcloud.com/server/stable/admin_manual/exapps_management/AppAPIAndExternalApps.html

What they give us:

- top-menu entries
- scripts/styles for ExApp-owned menu pages
- other AppAPI-managed UI surfaces

What they do **not** give us:

- a Talk meeting action insertion point
- a documented way to add a custom button into the Talk in-call menu

Conclusion:

- ExApps help with app-owned navigation surfaces
- they do not solve the Talk meeting-button problem directly

## Findings

### Finding 1: there is no documented public Talk UI extension point for a custom in-call button

The official Talk integration docs are backend-focused.
The official frontend extension docs show explicit hooks for some core surfaces, but not for Talk call controls.
The ExApp/AppAPI UI docs expose top-menu and app-owned UI surfaces, not Talk meeting controls.

So for D-263, we should assume:

- **there is currently no documented, stable public API for inserting a Cassini button directly into the Talk in-call controls**

### Finding 2: the narrowest viable documented mechanism is "load our script on logged-in pages"

The app bootstrapping and JavaScript docs clearly support:

- loading JS for logged-in users
- injecting initial state
- calling app routes with CSRF-aware requests

That gives us a supported delivery mechanism for our own integration code, even if the final DOM attachment inside Talk remains tactical.

### Finding 3: the right fallback is not "become a Talk recording backend"

For D-263, switching to Talk's recording-backend contract would change the product shape:

- different control boundary
- different Cassini API surface
- larger backend protocol work
- loss of the simple "Nextcloud app triggers Cassini" story

So if the in-call insertion point is fragile, the correct fallback is:

- move the trigger to a meeting-adjacent surface
- not re-scope the slice into Talk recording-backend work

## Selected direction

### D263-UI1

Use a standard PHP app plus a logged-in JS integration script.

The script should:

- detect when the user is on a Talk conversation / meeting page
- extract the room context needed for the app route
- render a small Cassini recording action in the narrowest stable practical location
- call a Nextcloud app route, not Cassini directly

### D263-UI2

Treat direct insertion into Talk's in-call top bar as a tactical shim, not a public integration contract.

That means:

- keep the code isolated in one frontend module
- avoid deep coupling across many Talk internals
- prefer one insertion rule and one fallback placement rather than many DOM variants

### D263-UI3

Keep the authoritative integration boundary on the server side.

The frontend action should only:

- send room context to the app route
- surface success/failure to the moderator

The app route should:

- validate permissions
- build/sign the Cassini trigger request
- call the external Cassini service through Nextcloud's HTTP client

## Proposed first-cut placement order

1. **Preferred**
   - inject a Cassini action into a meeting-adjacent part of the Talk page that is visible during the meeting flow and can reliably carry room context

2. **If feasible with acceptable fragility**
   - place the action near the in-call top-bar controls through a small DOM shim

3. **If the in-call DOM is too brittle**
   - fall back to a conversation-level or meeting-side-panel action that still starts recording for the current room

This ordering matters because the product requirement is:

- "start recording from Nextcloud meeting context"

It is **not**:

- "must be literally inside the exact native Talk top-bar control group"

## Why this is the right cut for D-263

This direction keeps the slice honest:

- it uses documented Nextcloud app loading mechanisms
- it avoids pretending Talk offers a public custom-button API when the docs do not show one
- it keeps the unsupported piece very small
- it preserves the selected product boundary where Cassini is triggered by our app, not by Talk's recording backend

## Concrete next implementation implications

1. The Nextcloud app should register a logged-in script load path in `Application.php` / bootstrap code.
2. The frontend should use initial state for:
   - app route base
   - whether Cassini is configured
   - any feature flag for the Talk integration
3. The frontend module should be written as a single Talk integration shim, not spread across generic app JS.
4. The frontend should tolerate "not on Talk" and "Talk loaded but unsupported layout" as normal no-op states.
5. The app route should be the only place that knows the Cassini URL and shared secret.

## Acceptance

This spike is complete because it answers the core shaping question:

- there is no documented public Talk-specific custom button API to target
- the supported way in is app script loading on logged-in pages
- therefore D-263 should use a narrow frontend shim plus a server-side app route
- if that shim is too brittle, the fallback is a meeting-adjacent trigger, not a backend-architecture change

## Reassessment

This spike does not eliminate all implementation risk.
It does eliminate the shape ambiguity.

The next uncertainty is no longer "what class of integration should we use?"
It is:

- **which exact Talk DOM/context signal is the least brittle hook in the target Nextcloud/Talk version**

That is now an implementation-detail spike or prototype task, not a higher-level shaping question.
