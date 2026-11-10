<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { readable, type Readable } from "svelte/store";
  import { ChevronDown, Ellipsis, Play, Square, X } from "@lucide/svelte";

  import { LL, numberFormat } from "../../i18n";
  import { countPeople, SHARED_DEVICE_LABEL, voiceNumber, voiceParent, type SpeakerGroup, type VoiceSample } from "../../core/speakers";
  import type { TranscriptSpeaker } from "../../core/types";
  import {
    applyPending,
    isSplit,
    labelProblem,
    peopleCountText,
    phaseText,
    progressNow,
    remainingText,
    sameEdits,
    speakerEditsLocked,
    unavailableReasonText,
    MAX_SPEAKER_LABEL_LENGTH,
  } from "../../viewer/speakerEdits";
  import { popover, stepIndex } from "../tags/popover";
  import { reloadDue, sinceAnswer, type SpeakersSession, type SpeakersState } from "./session";

  // The People section of the meeting details popover: every participant's
  // device, with the voices separated from a shared one listed beneath it.
  //
  // With a session (the app, with an operator behind it) it is also where the
  // speakers are changed: a device can be separated into voices or made one
  // person again, a participant or a voice named, and voices said to be the
  // same person. Without one
  // (an embed, a static export) it lists the speakers and offers nothing.
  export let groups: SpeakerGroup[] = [];
  export let session: SpeakersSession | null = null;
  export let samples: ReadonlyMap<string, VoiceSample> = new Map();
  export let playingSampleId: string | null = null;
  // Whether the meeting has a summary, and what rewriting it would cost: a
  // save keeps the summary unless someone allowed to asks for it to be
  // rewritten for the new names, at that cost (D-814).
  export let hasSummary = false;
  export let summaryCost: string | null = null;

  // Below this much speech a voice is often a fragment of another one.
  const LITTLE_SPEECH_MS = 10_000;

  const dispatch = createEventDispatcher<{ sample: { id: string } & VoiceSample; stopSample: void; reload: void }>();
  const off: Readable<SpeakersState> = readable({
    status: "off", server: null, shownRevision: null, shownSummary: null, pending: { labels: {}, merges: {} }, saving: false,
    applied: null, reloading: false, error: "", receivedAt: 0, now: 0,
  });

  let menu: { group: SpeakerGroup; anchor: HTMLElement } | null = null;
  let sameAs: { voice: TranscriptSpeaker; anchor: HTMLElement } | null = null;
  let menuEl: HTMLElement;

  $: store = session ?? off;
  $: server = $store.status === "ready" ? $store.server : null;
  $: editable = session !== null && server !== null;
  $: doc = server?.doc ?? null;
  $: pending = $store.pending;
  $: busy = $store.saving || server?.state === "applying";
  // The operator refuses every save on this meeting (its participant audio or
  // transcript is gone): the voices are shown as they are, with nothing to do.
  $: locked = Boolean(server && speakerEditsLocked(server));
  $: people = countPeople(groups);
  $: heading = peopleCountText(people);

  // Devices nobody separated, or asked to: each is one person, named on its
  // own row the way a voice is named on its.
  $: ownNamed = new Set(
    groups
      .filter((group) => group.speaks && group.voices.length === 0 && !(doc && isSplit(doc, group.device.id)))
      .map((group) => group.device.id),
  );
  // What each speaker is called now: the saved name when there is one (it may
  // not be applied yet), else the recording's own label.
  $: docLabels = new Map((doc?.labels ?? []).map((entry) => [entry.speakerId, entry.label]));
  $: shownLabels = new Map([
    ...groups
      .filter((group) => ownNamed.has(group.device.id))
      .map((group): [string, string] => [group.device.id, docLabels.get(group.device.id) ?? group.device.label]),
    ...groups.flatMap((group) => group.voices).map((voice): [string, string] => [voice.id, docLabels.get(voice.id) ?? voice.label]),
  ]);
  $: deviceLabels = new Map(groups.map((group) => [group.device.id, group.device.label]));
  $: savedMerges = new Map((doc?.merges ?? []).map((entry) => [entry.from, entry.into]));
  $: mergeOf = (id: string): string | null =>
    id in pending.merges ? pending.merges[id] : (savedMerges.get(id) ?? null);
  $: present = new Set(groups.flatMap((group) => group.voices.map((voice) => voice.id)));
  // Voices something is merged into, saved or picked: they cannot themselves
  // be merged away, since merges do not chain.
  $: targets = new Set(
    [...new Set([...present, ...savedMerges.keys()])].map((id) => mergeOf(id)).filter((id): id is string => Boolean(id)),
  );
  $: next = doc ? applyPending(doc, pending, shownLabels) : null;
  $: changed = Boolean(doc && next && !sameEdits(next, doc));
  $: problem = Object.values(pending.labels).map(labelProblem).find(Boolean) ?? "";
  $: separating = Boolean(
    doc?.splits.some(
      (split) =>
        !groups.some((group) => group.device.id === split.speakerId && group.voices.length > 0) &&
        !server?.report?.inconclusive.includes(split.speakerId),
    ),
  );
  // How far the apply is, from an operator that says so: its phase, the time
  // left on its estimate counted down since it answered, and the bar.
  $: progress =
    server?.state === "applying" && server.progress ? progressNow(server.progress, sinceAnswer($store)) : null;
  $: applyingText = phaseText(progress ? progress.phase : separating ? "separating" : "updating");
  // The time left changes every few seconds: shown, but left out of the live
  // status so a screen reader is not interrupted by it; the bar carries it.
  $: timeLeft = progress && progress.phase !== "queued" ? remainingText(progress.remainingMs) : "";
  // Names travel with the recording: said wherever voices can be named.
  // The voices the recording on screen has. Names and merges are shown on it
  // as soon as they are saved; only a different split, or a voice that is a
  // different person again, needs the republished recording ("Voices updated ·
  // Reload").
  $: shownVoices = new Set(groups.flatMap((group) => group.voices.map((voice) => voice.id)));
  $: stale = reloadDue($store, shownVoices);
  $: nameable = editable && !locked && groups.some((group) => group.voices.length > 0 || ownNamed.has(group.device.id));
  $: summaryStale = server?.report?.summary === "stale";
  // Offered with the Save of a change, to those the operator lets rewrite a
  // summary; off by default, because it costs a model call.
  let rewriteSummary = false;
  $: rewriteOffered = Boolean(changed && hasSummary && server?.canRewriteSummary);
  $: if (!rewriteOffered) rewriteSummary = false;

  async function save() {
    const asked = rewriteOffered && rewriteSummary;
    const saved = await session?.saveEdits(shownLabels, asked ? "rewrite" : "keep");
    if (saved) rewriteSummary = false;
  }
  $: inconclusive = (server?.report?.inconclusive ?? []).filter(
    (id) => doc && isSplit(doc, id) && !groups.some((group) => group.device.id === id && group.voices.length > 0),
  );

  function initials(name: string): string {
    const parts = name.trim().split(/\s+/).filter(Boolean);
    return ((parts[0]?.[0] ?? "") + (parts.length > 1 ? (parts.at(-1)?.[0] ?? "") : "")).toUpperCase() || "?";
  }

  // A device's name as shown: the English placeholder for a separated device
  // with no name of its own is put in the reader's language here.
  function deviceName(label: string): string {
    return label === SHARED_DEVICE_LABEL ? $LL.viewer.meeting.people.shared_device() : label;
  }

  // "Speaker 2": a voice inside its device's group.
  function voiceShort(id: string): string {
    return $LL.viewer.meeting.people.voice({ number: numberFormat().format(voiceNumber(id)) });
  }

  // "Same person as {name}", with the name in bold: the whole sentence is
  // translated, then cut where the name goes.
  function mergedIntoParts(): [string, string] {
    const marker = "\u0000";
    const [before = "", after = ""] = $LL.viewer.meeting.people.merged_into({ name: marker }).split(marker);
    return [before, after];
  }

  // A voice's default label, as the producer writes it. Not translated: it is
  // compared with what the operator wrote.
  function defaultLabel(id: string): string {
    return `${deviceLabels.get(voiceParent(id)) ?? SHARED_DEVICE_LABEL} · Speaker ${voiceNumber(id)}`;
  }

  function voiceInitials(voice: TranscriptSpeaker): string {
    const label = shownLabels.get(voice.id) ?? voice.label;
    return label === defaultLabel(voice.id) ? String(voiceNumber(voice.id)) : initials(label);
  }

  // What a voice is called on its controls: the saved name, so a field's
  // accessible name does not change under the reader's typing.
  function labelOf(id: string): string {
    return shownLabels.get(id) ?? docLabels.get(id) ?? defaultLabel(id);
  }

  // What a voice is called where another voice refers to it: the name being
  // typed, so "Same person as" offers the names the reader just gave.
  function nameOf(id: string): string {
    return pending.labels[id]?.trim() || labelOf(id);
  }

  // Inside its device's group a voice needs no device name: "Speaker 2".
  function shortName(id: string): string {
    const name = nameOf(id);
    return name === defaultLabel(id) ? voiceShort(id) : name;
  }

  function isGroupSplit(group: SpeakerGroup): boolean {
    return group.voices.length > 0 || Boolean(doc && isSplit(doc, group.device.id));
  }

  function siblings(voice: TranscriptSpeaker): TranscriptSpeaker[] {
    const group = groups.find((candidate) => candidate.device.id === voiceParent(voice.id));
    return (group?.voices ?? []).filter((other) => other.id !== voice.id && !mergeOf(other.id));
  }

  // Merged voices the recording no longer has: they live on as part of the
  // voice they were merged into, and can be separated from it again. One the
  // reader has just said is not the same person leaves the list at once, so
  // the click shows; Save (or Cancel, which brings it back) settles it.
  function absorbed(voice: TranscriptSpeaker): string[] {
    return [...savedMerges.keys()].filter((from) => mergeOf(from) === voice.id && !present.has(from));
  }

  function toggleMenu(group: SpeakerGroup, anchor: HTMLElement) {
    sameAs = null;
    menu = menu?.anchor === anchor ? null : { group, anchor };
  }

  function separate(group: SpeakerGroup) {
    menu = null;
    void session?.separate(group.device.id, shownLabels);
  }

  function unsplit(group: SpeakerGroup) {
    menu = null;
    void session?.unsplit(group.device.id, shownLabels);
  }

  function pickSameAs(voice: TranscriptSpeaker, into: TranscriptSpeaker) {
    sameAs = null;
    session?.setMerge(voice.id, into.id);
  }

  function playOrStop(voice: TranscriptSpeaker) {
    const sample = samples.get(voice.id);
    if (playingSampleId === voice.id) dispatch("stopSample");
    else if (sample) dispatch("sample", { id: voice.id, ...sample });
  }

  function onMenuKeydown(event: KeyboardEvent) {
    const items = [...menuEl.querySelectorAll<HTMLButtonElement>("button:not(:disabled)")];
    const step = stepIndex(items.indexOf(event.target as HTMLButtonElement), event.key, items.length);
    if (step !== null) {
      event.preventDefault();
      items[step].focus();
    }
  }

  const focusFirst = (node: HTMLElement) => {
    node.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
  };
</script>

{#if groups.length > 0}
  <div class="mf-people" class:pp-editable={editable}>
    <p class="mf-heading">{heading}</p>
    <ul class="pp-list">
      {#each groups as group (group.device.id)}
        {@const split = isGroupSplit(group)}
        <li class="pp-device" class:pp-shared={split && !group.speaks}>
          <span class="mf-avatar" aria-hidden="true">{initials(deviceName(group.device.label))}</span>
          {#if editable && ownNamed.has(group.device.id)}
            {@const label = labelOf(group.device.id)}
            <input
              type="text"
              class="input input-xs pp-input"
              aria-label={$LL.viewer.meeting.people.name_for({ name: deviceName(label) })}
              maxlength={MAX_SPEAKER_LABEL_LENGTH}
              autocomplete="off"
              spellcheck="false"
              placeholder={$LL.viewer.meeting.people.add_name()}
              disabled={locked}
              value={pending.labels[group.device.id] ?? label}
              on:input={(event) => session?.setLabel(group.device.id, event.currentTarget.value)}
              on:focus={(event) => event.currentTarget.select()}
            />
          {:else}
            <span class="pp-name truncate">{deviceName(group.device.label)}</span>
          {/if}
          {#if editable}
            <button
              type="button"
              class="pp-icon"
              aria-label={$LL.viewer.meeting.people.actions_for({ name: deviceName(group.device.label) })}
              aria-haspopup="menu"
              aria-expanded={menu?.group.device.id === group.device.id}
              on:click={(event) => toggleMenu(group, event.currentTarget)}
            >
              <Ellipsis size={15} aria-hidden="true" />
            </button>
          {/if}
        </li>
        {#each group.voices as voice (voice.id)}
          {@const label = labelOf(voice.id)}
          {@const into = mergeOf(voice.id)}
          {@const sample = samples.get(voice.id)}
          {#if editable}
            <li class="pp-voice">
              <div class="pp-voice-main">
                <button
                  type="button"
                  class="pp-icon pp-play"
                  aria-label={playingSampleId === voice.id ? $LL.viewer.meeting.people.stop_sample({ name: label }) : $LL.viewer.meeting.people.play_sample({ name: label })}
                  aria-pressed={playingSampleId === voice.id}
                  title={playingSampleId === voice.id ? $LL.viewer.meeting.people.stop() : $LL.viewer.meeting.people.listen_title()}
                  disabled={!sample}
                  on:click={() => playOrStop(voice)}
                >
                  {#if playingSampleId === voice.id}
                    <Square size={11} fill="currentColor" aria-hidden="true" />
                  {:else}
                    <Play size={12} fill="currentColor" aria-hidden="true" />
                  {/if}
                </button>
                {#if into}
                  {@const [mergedBefore, mergedAfter] = mergedIntoParts()}
                  <span class="pp-merged truncate">{mergedBefore}<strong>{shortName(into)}</strong>{mergedAfter}</span>
                  <button
                    type="button"
                    class="pp-icon"
                    aria-label={$LL.viewer.meeting.people.not_same_as({ name: label, other: shortName(into) })}
                    title={$LL.viewer.meeting.people.not_same()}
                    disabled={busy || locked}
                    on:click={() => session?.setMerge(voice.id, null)}
                  >
                    <X size={13} aria-hidden="true" />
                  </button>
                {:else}
                  <input
                    type="text"
                    class="input input-xs pp-input"
                    aria-label={$LL.viewer.meeting.people.name_for({ name: label })}
                    maxlength={MAX_SPEAKER_LABEL_LENGTH}
                    autocomplete="off"
                    spellcheck="false"
                    placeholder={$LL.viewer.meeting.people.voice_placeholder({ number: numberFormat().format(voiceNumber(voice.id)) })}
                    disabled={locked}
                    value={pending.labels[voice.id] ?? (label === defaultLabel(voice.id) ? "" : label)}
                    on:input={(event) => session?.setLabel(voice.id, event.currentTarget.value)}
                    on:focus={(event) => event.currentTarget.select()}
                  />
                {/if}
              </div>
              {#if (!into && !targets.has(voice.id) && siblings(voice).length > 0) || (sample && sample.speechMs < LITTLE_SPEECH_MS)}
                <div class="pp-voice-meta">
                  {#if !into && !targets.has(voice.id) && siblings(voice).length > 0}
                    <button
                      type="button"
                      class="pp-same"
                      aria-haspopup="menu"
                      aria-expanded={sameAs?.voice.id === voice.id}
                      aria-label={$LL.viewer.meeting.people.same_as_label({ name: label })}
                      disabled={locked}
                      on:click={(event) => {
                        menu = null;
                        sameAs = sameAs?.voice.id === voice.id ? null : { voice, anchor: event.currentTarget };
                      }}
                    >
                      {$LL.viewer.meeting.people.same_as_button()} <ChevronDown size={11} aria-hidden="true" />
                    </button>
                  {/if}
                  {#if sample && sample.speechMs < LITTLE_SPEECH_MS}
                    <span class="pp-hint">{$LL.viewer.meeting.people.little_speech()}</span>
                  {/if}
                </div>
              {/if}
              {#each absorbed(voice) as from (from)}
                <p class="pp-voice-meta pp-absorbed">
                  <span class="truncate">{$LL.viewer.meeting.people.includes({ name: shortName(from) })}</span>
                  <button type="button" class="link" disabled={busy || locked} on:click={() => session?.setMerge(from, null)}>
                    {$LL.viewer.meeting.people.not_same()}
                  </button>
                </p>
              {/each}
            </li>
          {:else}
            <li class="pp-voice pp-voice-read">
              <span class="mf-avatar pp-voice-avatar" aria-hidden="true">{voiceInitials(voice)}</span>
              <span class="truncate">{voice.label}</span>
            </li>
          {/if}
        {/each}
      {/each}
    </ul>
    {#if nameable}
      <p class="pp-privacy">{$LL.viewer.meeting.people.privacy()}</p>
    {/if}

    {#if editable && server}
      {#if $store.error || problem || inconclusive.length > 0 || summaryStale || $store.saving || server.state !== "idle" || $store.reloading || stale || changed}
        <div class="pp-footer">
          {#if summaryStale}
            <p class="pp-note" role="status">
              {$LL.viewer.meeting.people.summary_stale()}{#if !server.canRewriteSummary}
                {$LL.viewer.meeting.people.summary_stale_who()}{/if}
            </p>
          {/if}
          {#each inconclusive as id (id)}
            <p class="pp-note" role="status">
              {$LL.viewer.meeting.people.inconclusive({ device: deviceName(deviceLabels.get(id) ?? id) })}
            </p>
          {/each}
          {#if $store.error}
            <p class="pp-note text-error" role="alert">
              {$store.error}
              <button type="button" class="link" on:click={() => session?.dismissError()}>{$LL.viewer.meeting.people.dismiss()}</button>
            </p>
          {:else if problem}
            <p class="pp-note text-error" role="alert">{problem}</p>
          {/if}
          <div class="pp-status-row">
            <p class="pp-status" role="status">
              {#if $store.saving}
                <span class="cassini-spinner pp-spinner" aria-hidden="true"></span>{$LL.viewer.meeting.people.saving()}
              {:else if server.state === "applying"}
                <span class="cassini-spinner pp-spinner" aria-hidden="true"></span>{applyingText}
                {#if timeLeft}<span aria-hidden="true">{timeLeft}</span>{/if}
              {:else if server.state === "failed"}
                <span title={server.lastError || undefined}>{$LL.viewer.meeting.people.update_failed()}</span> ·
                <button type="button" class="link" on:click={() => session?.retry()}>{$LL.viewer.meeting.people.retry()}</button>
              {:else if $store.reloading}
                <span class="cassini-spinner pp-spinner" aria-hidden="true"></span>{$LL.viewer.meeting.people.loading_updated()}
              {:else if stale}
                {$LL.viewer.meeting.people.voices_updated()} ·
                <button type="button" class="link" on:click={() => dispatch("reload")}>{$LL.viewer.meeting.people.reload()}</button>
              {/if}
            </p>
            {#if changed}
              {#if rewriteOffered}
                <label class="pp-rewrite">
                  <input type="checkbox" class="checkbox checkbox-xs" bind:checked={rewriteSummary} disabled={busy || locked} />
                  <span>
                    {$LL.viewer.meeting.people.rewrite_summary()}
                    {#if summaryCost}<span class="pp-rewrite-cost">{summaryCost}</span>{/if}
                  </span>
                </label>
              {/if}
              <div class="pp-actions">
                <button type="button" class="btn btn-ghost btn-xs" on:click={() => session?.discard()}>{$LL.viewer.meeting.people.cancel()}</button>
                <button
                  type="button"
                  class="btn btn-neutral btn-xs"
                  disabled={busy || locked || Boolean(problem)}
                  title={busy ? $LL.viewer.meeting.people.wait_title() : undefined}
                  on:click={save}
                >
                  {$LL.viewer.meeting.people.save()}
                </button>
              </div>
            {/if}
          </div>
          {#if progress && timeLeft && !$store.saving}
            <div
              class="pp-progress"
              role="progressbar"
              aria-label={phaseText(progress.phase).replace("…", "")}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={progress.percent}
              aria-valuetext={timeLeft}
            >
              <span class="pp-progress-fill" style:width={`${progress.percent}%`}></span>
            </div>
          {/if}
        </div>
      {/if}
    {/if}
  </div>

  {#if menu}
    {@const group = menu.group}
    <div
      bind:this={menuEl}
      use:popover={{ anchor: menu.anchor, close: () => (menu = null) }}
      use:focusFirst
      role="menu"
      tabindex="-1"
      aria-label={$LL.viewer.meeting.people.actions_for({ name: deviceName(group.device.label) })}
      class="tag-popover grid w-60 p-1"
      on:keydown={onMenuKeydown}
    >
      {#if isGroupSplit(group)}
        <button type="button" role="menuitem" class="pp-item" disabled={busy || locked} on:click={() => unsplit(group)}>
          {$LL.viewer.meeting.people.treat_as_one()}
        </button>
      {:else}
        <button
          type="button"
          role="menuitem"
          class="pp-item pp-item-two"
          aria-label={$LL.viewer.meeting.people.separate()}
          aria-describedby="pp-separate-why"
          disabled={busy || !server?.available}
          on:click={() => separate(group)}
        >
          <span>{$LL.viewer.meeting.people.separate()}</span>
          <span id="pp-separate-why" class="pp-item-sub">{$LL.viewer.meeting.people.separate_why()}</span>
        </button>
      {/if}
      <!-- Without the diarizer a separated device can still be made one
           person again; any other reason stops both. -->
      {#if server && server.reason && (locked || !isGroupSplit(group))}
        <p class="pp-item-note">{unavailableReasonText(server.reason)}</p>
      {/if}
      {#if busy}
        <p class="pp-item-note">{$LL.viewer.meeting.people.wait_note()}</p>
      {/if}
    </div>
  {/if}

  {#if sameAs}
    {@const voice = sameAs.voice}
    <div
      bind:this={menuEl}
      use:popover={{ anchor: sameAs.anchor, close: () => (sameAs = null) }}
      use:focusFirst
      role="menu"
      tabindex="-1"
      aria-label={$LL.viewer.meeting.people.same_as_menu({ name: labelOf(voice.id) })}
      class="tag-popover grid w-56 p-1"
      on:keydown={onMenuKeydown}
    >
      {#each siblings(voice) as other (other.id)}
        <button type="button" role="menuitem" class="pp-item" on:click={() => pickSameAs(voice, other)}>
          {shortName(other.id)}
        </button>
      {/each}
    </div>
  {/if}
{/if}

<style>
  /* The section shares the popover's own rules (MeetingFacts.svelte): the
     heading, the list and the avatars look as they always did, and only what
     is new is styled here. */
  .mf-people {
    flex: none;
    display: flex;
    flex-direction: column;
    padding-top: 8px;
  }
  .mf-heading {
    flex: none;
    margin-bottom: 6px;
    padding-inline: 12px;
    font-size: 12px;
    font-weight: 600;
    color: var(--mf-muted);
  }
  .pp-list {
    display: grid;
    gap: 6px;
    min-height: 0;
    max-height: 220px;
    padding: 0 12px 10px;
    overflow-y: auto;
    overscroll-behavior: contain;
  }
  .pp-editable .pp-list {
    max-height: 340px;
  }
  .pp-list li {
    min-width: 0;
    color: var(--mf-strong);
  }
  .pp-device {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .pp-name {
    flex: 1;
    min-width: 0;
  }
  /* A device whose words all went to its voices is a heading for them. */
  .pp-shared .pp-name {
    color: var(--mf-muted);
    font-weight: 550;
  }
  .mf-avatar {
    flex: none;
    display: inline-grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 999px;
    background-color: var(--mf-tint);
    font-size: 10px;
    font-weight: 600;
    color: var(--mf-strong);
  }

  /* Voices hang off their device on a rule, the way a reply hangs off a post. */
  .pp-voice {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 3px;
    margin-left: 10px;
    padding-left: 18px;
    border-left: 1px solid var(--color-base-300);
  }
  .pp-voice-read {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .pp-voice-avatar {
    width: 20px;
    height: 20px;
  }
  .pp-voice-main {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .pp-input {
    flex: 1;
    width: auto;
    min-width: 0;
    font-size: 13px;
  }
  .pp-merged {
    flex: 1;
    min-width: 0;
    font-size: 12.5px;
    color: var(--mf-muted);
  }
  .pp-merged strong {
    font-weight: 600;
    color: var(--mf-strong);
  }
  .pp-voice-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 10px;
    padding-left: 30px;
    font-size: 11.5px;
    color: var(--mf-muted);
  }
  .pp-absorbed {
    flex-wrap: nowrap;
  }
  .pp-absorbed .link {
    flex: none;
  }
  .pp-same {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    padding: 1px 4px;
    margin-left: -4px;
    border: 0;
    border-radius: var(--radius-field, 0.25rem);
    background: none;
    font: inherit;
    font-weight: 600;
    color: var(--mf-muted);
    cursor: pointer;
  }
  .pp-same:hover,
  .pp-same[aria-expanded="true"] {
    background-color: var(--mf-tint);
    color: var(--mf-strong);
  }
  .pp-hint {
    font-style: italic;
  }

  .pp-icon {
    flex: none;
    display: inline-grid;
    place-items: center;
    width: 24px;
    height: 24px;
    border: 0;
    border-radius: 5px;
    background: none;
    color: var(--mf-muted);
    cursor: pointer;
  }
  .pp-icon:hover:not(:disabled),
  .pp-icon[aria-expanded="true"] {
    background-color: var(--mf-tint);
    color: var(--mf-strong);
  }
  .pp-icon:focus-visible,
  .pp-same:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 1px;
  }
  .pp-icon:disabled {
    opacity: 0.4;
    cursor: default;
  }
  .pp-play {
    border: 1px solid var(--color-base-300);
    border-radius: 999px;
  }
  .pp-play[aria-pressed="true"] {
    border-color: var(--color-primary);
    color: var(--color-primary);
  }

  .pp-privacy {
    flex: none;
    margin-top: -4px;
    padding: 0 12px 8px;
    font-size: 11.5px;
    line-height: 1.4;
    color: var(--mf-muted);
  }
  .pp-rewrite {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    flex-basis: 100%;
    font-size: 12px;
    line-height: 1.4;
    color: var(--mf-strong);
    cursor: pointer;
  }
  .pp-rewrite input {
    margin-top: 1px;
  }
  .pp-rewrite-cost {
    display: block;
    color: var(--mf-muted);
  }
  .pp-actions {
    display: flex;
    flex: none;
    justify-content: flex-end;
    gap: 6px;
  }

  .pp-footer {
    flex: none;
    display: grid;
    gap: 6px;
    padding: 8px 12px 10px;
    border-top: 1px solid var(--color-base-300);
    font-size: 12px;
  }
  .pp-note {
    line-height: 1.4;
    color: var(--mf-muted);
  }
  .pp-status-row {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 24px;
  }
  .pp-status {
    flex: 1;
    min-width: 0;
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0 5px;
    color: var(--mf-muted);
  }
  .pp-spinner {
    width: 12px;
    height: 12px;
    border-width: 2px;
  }
  /* A slim bar under the status: how much of the operator's estimate has
     gone. It moves once a second, so a linear glide makes it read as steady. */
  .pp-progress {
    height: 3px;
    margin-top: -2px;
    overflow: hidden;
    border-radius: 999px;
    background-color: var(--mf-tint);
  }
  .pp-progress-fill {
    display: block;
    height: 100%;
    border-radius: inherit;
    background-color: var(--color-primary);
    transition: width 1s linear;
  }
  @media (prefers-reduced-motion: reduce) {
    .pp-progress-fill {
      transition: none;
    }
  }

  .pp-item {
    padding: 6px 10px;
    border-radius: var(--radius-field, 0.25rem);
    font-size: 13px;
    text-align: left;
    color: var(--color-base-content);
    cursor: pointer;
  }
  .pp-item:hover:not(:disabled),
  .pp-item:focus-visible {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
    outline: none;
  }
  .pp-item-two {
    display: grid;
    gap: 1px;
  }
  .pp-item-sub {
    font-size: 11.5px;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .pp-item:disabled {
    opacity: 0.4;
    cursor: default;
  }
  .pp-item-note {
    padding: 2px 10px 6px;
    font-size: 11.5px;
    line-height: 1.4;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
</style>
