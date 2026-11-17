<script lang="ts">
  import { createEventDispatcher, onDestroy } from "svelte";
  import { Info, TriangleAlert } from "@lucide/svelte";
  import { LL, operatorPlace } from "cassini-viewer/i18n";
  import type { SetupNotice, SetupNoticeTone } from "./operator/setupHealth";

  // What the Cassini shell shows when recordings cannot be saved. It renders a
  // decision it did not make: every word and the tone it is drawn in come from
  // buildSetupNotice (setupHealth.ts), which is where the copy is tested. What
  // belongs here is presentation only.
  //
  // One strip above the meeting list, which stays readable whatever the check
  // says (D-849), and one row tall (#500). It says what is wrong and offers one
  // way on. The steps to fix it live in Operator › Doctor, so an administrator
  // is sent there rather than handed a recipe in a banner; everyone else gets
  // what still works and a link to pass to someone who can fix it.
  export let notice: SetupNotice;

  // The tone is the notice's, not the component's: a warning triangle over "a
  // check has not run yet" is the shell shouting about a state that is ordinary
  // after every container restart. The default is the loud one, so a caller
  // that says nothing gets the safe answer rather than a quiet failure.
  export let tone: SetupNoticeTone = "warning";

  // True while the re-check the Try again button asked for is still running.
  // The shell owns that request — it is the one holding the operator client —
  // so it also owns saying when it is finished.
  export let busy: boolean = false;

  // The shell owns which surface is showing and the operator client, so both
  // buttons ask rather than act.
  const dispatch = createEventDispatcher<{ navigate: "doctor"; retry: void }>();

  // Only an administrator's notice carries a cause, so it is also what says
  // which audience this draws for. Everyone else gets no Try again: it would
  // re-run a check whose answer they are not shown and could not act on.
  $: isAdmin = Boolean(notice.cause);

  // "copied" for a moment after copying; "failed" when the browser refused,
  // and then the address is shown to copy by hand.
  let copyState: "" | "copied" | "failed" = "";
  let copyTimer: ReturnType<typeof setTimeout> | undefined;
  async function copyLink(): Promise<void> {
    clearTimeout(copyTimer);
    try {
      await navigator.clipboard.writeText(notice.shareUrl);
      copyState = "copied";
      copyTimer = setTimeout(() => (copyState = ""), 2000);
    } catch {
      copyState = "failed";
    }
  }
  onDestroy(() => clearTimeout(copyTimer));
</script>

<div class="px-3 py-2">
  <section
    class="alert items-center gap-3 py-2 grid-cols-[auto_minmax(0,1fr)] {tone === 'warning' ? 'alert-warning' : ''}"
    role="status"
  >
    {#if tone === "warning"}
      <TriangleAlert size={16} class="shrink-0" aria-hidden="true" />
    {:else}
      <Info size={16} class="shrink-0 opacity-70" aria-hidden="true" />
    {/if}
    <div class="flex w-full min-w-0 flex-col gap-1">
      <!-- The account on the left, the action on the right, wrapping under it
           only when the strip is too narrow. -->
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
        <p class="min-w-0 flex-1 basis-64 text-sm">
          <span class="font-semibold">{notice.title}.</span>
          {#if isAdmin}{notice.cause}{:else}{notice.summary}{/if}
        </p>
        <div class="flex shrink-0 gap-2">
          {#if isAdmin}
            <button class="btn btn-xs" type="button" disabled={busy} on:click={() => dispatch("retry")}>
              {busy ? $LL.app.shell.setup_notice.checking() : $LL.common.action.try_again()}
            </button>
            <button class="btn btn-xs" type="button" on:click={() => dispatch("navigate", "doctor")}>
              {$LL.app.shell.setup_notice.open_doctor({ place: operatorPlace("doctor") })}
            </button>
          {:else if notice.shareUrl}
            <button class="btn btn-xs" type="button" on:click={copyLink}>
              {copyState === "copied" ? $LL.common.status.copied() : $LL.app.shell.setup_notice.copy_link()}
            </button>
          {/if}
        </div>
      </div>
      {#if copyState === "failed"}
        <a class="link min-w-0 truncate text-sm" href={notice.shareUrl} title={notice.shareUrl}>{notice.shareUrl}</a>
      {/if}
    </div>
  </section>
</div>
