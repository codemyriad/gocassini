<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { Check, Copy, KeyRound } from "@lucide/svelte";
  import { LL } from "cassini-viewer/i18n";

  // The one time the service account's password is on screen (D-708).
  //
  // Cassini's operator authenticates as this account through AppAPI's
  // act-as-user header, signed with the app secret, so it needs no password of
  // its own — and one on the operator's volume would be a credential at rest for
  // an account nothing authenticates as. The value is generated in this browser,
  // set through Nextcloud's own provisioning API on the administrator's session,
  // and lives only in this component's props until it is dismissed.
  //
  // So the dismissal is deliberate, not a close button. There is no second
  // chance at this string: the only way back is a reset, which mints a different
  // one. Requiring an explicit acknowledgement is the difference between an
  // administrator who has it and one who finds out in a month that they do not.

  export let user: string;
  export let password: string;
  // resetOcc is the operator's own command line, for an administrator who would
  // rather not rely on having copied this.
  export let resetOcc = "";

  const dispatch = createEventDispatcher<{ acknowledge: void }>();

  // A sentence with the account name in it, cut where {user} goes so the name
  // can be drawn as <code> without piecing the sentence together.
  const MARK = "\u0000";
  function around(sentence: (args: { user: string }) => string): [string, string] {
    const [before, after = ""] = sentence({ user: MARK }).split(MARK);
    return [before, after];
  }
  $: t = $LL.app.settings.password_reveal;
  $: saveNow = around(t.save_now);
  $: onlyTime = around(t.only_time);

  let copied = false;
  let copyFailed = false;
  let field: HTMLInputElement | null = null;

  // The clipboard is not a given here.
  //
  // The app runs inside a shadow root on AppAPI's embedded page, where
  // `navigator.clipboard` needs both a secure context and `clipboard-write` in
  // the frame's permissions policy. Neither is guaranteed, so there are three
  // layers: the async API, `execCommand` over a selection, and — always — a
  // readonly field the administrator can select by hand. A copy button that
  // silently does nothing is worse than no button.
  async function copy(): Promise<void> {
    copyFailed = false;
    try {
      await navigator.clipboard.writeText(password);
      copied = true;
      return;
    } catch {
      // Fall through to the selection-based path.
    }
    try {
      field?.select();
      field?.setSelectionRange(0, password.length);
      if (document.execCommand("copy")) {
        copied = true;
        return;
      }
    } catch {
      // Nothing left to try.
    }
    copyFailed = true;
    field?.select();
  }
</script>

<!-- A generic <div>, not a <section>: an interactive role on a semantic
     sectioning element is an a11y error, and this genuinely is a dialog rather
     than a region of the page. Same reasoning as the settings section's own
     confirmations. -->
<div
  class="grid gap-3 rounded-box border border-warning bg-warning/10 p-3"
  role="alertdialog"
  aria-label={t.dialog_label()}
>
  <div class="flex items-start gap-2">
    <KeyRound size={18} class="mt-0.5 shrink-0 text-warning" aria-hidden="true" />
    <div class="grid gap-1">
      <p class="text-sm font-semibold">
        {saveNow[0]}<code>{user}</code>{saveNow[1]}
      </p>
      <p class="text-xs break-words text-base-content/80">
        {onlyTime[0]}<code>{user}</code>{onlyTime[1]}
      </p>
    </div>
  </div>

  <div class="flex flex-wrap items-center gap-2">
    <!-- Readonly rather than hidden behind a reveal toggle: this is already the
         one moment it is meant to be visible, and a field the administrator can
         select by hand is the fallback for every clipboard that refuses. -->
    <input
      class="input input-sm input-bordered w-full max-w-md font-mono text-xs"
      type="text"
      readonly
      bind:this={field}
      value={password}
      aria-label={t.field_label()}
      on:focus={(event) => event.currentTarget.select()}
    />
    <button class="btn btn-sm" type="button" on:click={copy}>
      {#if copied}
        <Check size={14} aria-hidden="true" />
        {$LL.common.status.copied()}
      {:else}
        <Copy size={14} aria-hidden="true" />
        {t.copy()}
      {/if}
    </button>
  </div>

  {#if copyFailed}
    <p class="text-xs break-words text-warning" role="status">
      {t.copy_blocked()}
    </p>
  {/if}

  {#if resetOcc}
    <details class="text-xs">
      <summary class="cursor-pointer text-base-content/70">{t.set_on_server()}</summary>
      <pre
        class="m-0 mt-1 overflow-x-auto rounded-box bg-base-200 p-2 font-mono text-xs leading-relaxed">{resetOcc}</pre>
    </details>
  {/if}

  <button class="btn btn-sm btn-warning w-fit" type="button" on:click={() => dispatch("acknowledge")}>
    {t.acknowledge()}
  </button>
</div>
