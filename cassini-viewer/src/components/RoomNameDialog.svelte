<script lang="ts">
  import { createEventDispatcher, onMount, tick } from "svelte";
  import type { DataProvider } from "../viewer/dataProvider";
  import { RoomNameError, validateRoomName, type RoomNameState } from "../viewer/roomNames";

  export let roomId: string;
  export let provider: DataProvider;
  const dispatch = createEventDispatcher<{ close: void; saved: RoomNameState; unavailable: void }>();
  let dialog: HTMLDialogElement;
  let input: HTMLInputElement;
  let state: RoomNameState | null = null;
  let draft = "";
  let error = "";
  let busy = false;
  let loading = true;
  let uncertain = false;
  let inaccessible = false;
  let disposed = false;
  let backdropPressed = false;
  $: validation = validateRoomName(draft);

  onMount(() => {
    const previous = (dialog.getRootNode() as Document | ShadowRoot).activeElement as HTMLElement | null;
    dialog.showModal();
    void load();
    return () => { disposed = true; dialog.close(); previous?.focus(); };
  });

  async function load() {
    loading = true;
    error = "";
    try {
      const current = await provider.loadRoomName!(roomId);
      if (disposed) return;
      state = current;
      draft = current.displayName;
      loading = false;
      await tick(); input?.focus(); input?.select();
    } catch (cause) { error = cause instanceof Error ? cause.message : String(cause); }
    finally { loading = false; }
  }

  async function save(reset = false) {
    if (!state || busy || inaccessible || (!reset && validation)) return;
    busy = true; error = "";
    try {
      if (uncertain) {
        state = await provider.loadRoomName!(roomId);
        uncertain = false;
        error = `Current saved name: ${state.displayName}. Review it, then save again.`;
        return;
      }
      const saved = await provider.saveRoomName!(roomId, reset ? null : draft.trim(), state.revision);
      if (!disposed) dispatch("saved", saved);
    } catch (cause) {
      if (disposed) return;
      error = cause instanceof Error ? cause.message : String(cause);
      if (cause instanceof RoomNameError && cause.current) {
        state = cause.current;
        error += ` Current saved name: ${state.displayName}. Your draft is unchanged.`;
      } else if (cause instanceof RoomNameError && cause.status === 404) {
        inaccessible = true; dispatch("unavailable");
      } else if (!(cause instanceof RoomNameError) || cause.status === null || cause.status >= 500) {
        uncertain = true;
      }
    } finally { busy = false; }
  }

  function cancel(event?: Event) {
    event?.preventDefault();
    if (!busy) dispatch("close");
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key !== "Escape") return;
    // Handle this before the embedding page's global shortcuts can cancel
    // the browser's native dialog dismissal.
    event.stopPropagation();
    cancel(event);
  }

  function isBackdrop(event: MouseEvent) {
    if (event.target !== dialog) return false;
    const bounds = dialog.getBoundingClientRect();
    return event.clientX < bounds.left || event.clientX > bounds.right ||
      event.clientY < bounds.top || event.clientY > bounds.bottom;
  }

  function handleBackdropClick(event: MouseEvent) {
    const dismiss = backdropPressed && isBackdrop(event);
    backdropPressed = false;
    if (dismiss) cancel();
  }
</script>

<dialog bind:this={dialog} aria-labelledby="room-name-title" on:cancel={cancel} on:keydown={handleKeydown}
  on:pointerdown={(event) => (backdropPressed = event.button === 0 && isBackdrop(event))}
  on:pointercancel={() => (backdropPressed = false)} on:click={handleBackdropClick} class="room-dialog">
  <form on:submit|preventDefault={() => save()}>
    <h2 id="room-name-title">Rename room</h2>
    {#if loading}
      <p role="status">Loading room name…</p>
    {:else if state}
      <label for="room-name-input">Room name</label>
      <input bind:this={input} id="room-name-input" bind:value={draft} disabled={busy || inaccessible} autocomplete="off" aria-describedby="room-name-scope room-name-error" />
      <p class="secondary">Default: {state.defaultName || state.roomId}</p>
      <p id="room-name-scope">This name is shared with everyone who can access this room in Cassini.</p>
    {/if}
    <p id="room-name-error" role="alert">{error || (state && validation ? validation : "")}</p>
    <div class="actions">
      {#if state?.customName !== null && state}<button type="button" disabled={busy || inaccessible} on:click={() => save(true)}>Use default</button>{/if}
      <span class="spacer"></span>
      <button type="button" disabled={busy} on:click={() => cancel()}>Cancel</button>
      {#if state}<button class="save" type="submit" disabled={busy || inaccessible || !!validation}>{busy ? "Saving…" : "Save"}</button>
      {:else if !loading}<button type="button" on:click={load}>Retry</button>{/if}
    </div>
  </form>
</dialog>

<style>
  .room-dialog { margin: auto; width: min(28rem, calc(100vw - 2rem)); padding: 1.5rem; border: 1px solid var(--color-base-300); border-radius: 0.75rem; background: var(--color-base-100); color: var(--color-base-content); box-shadow: 0 12px 48px #0004; }
  .room-dialog::backdrop { background: #0006; }
  h2 { font-size: 1.125rem; font-weight: 600; margin-bottom: 1rem; }
  label { display: block; font-size: 0.875rem; margin-bottom: 0.375rem; }
  input { width: 100%; padding: 0.5rem 0.75rem; border: 1px solid var(--color-base-300); border-radius: 0.375rem; background: var(--color-base-200); }
  p { margin-top: 0.75rem; font-size: 0.8125rem; }
  .secondary { opacity: 0.7; }
  [role="alert"] { color: var(--color-error); }
  .actions { display: flex; align-items: center; gap: 0.5rem; margin-top: 1.25rem; }
  .spacer { flex: 1; }
  button { border-radius: 0.375rem; padding: 0.375rem 0.625rem; cursor: pointer; background: var(--color-base-200); }
  button:disabled { opacity: 0.5; cursor: default; }
  .save { background: var(--color-primary); color: var(--color-primary-content); }
</style>
