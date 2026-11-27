<script lang="ts">
  // Operator › Webhooks (#488): the one receiver Cassini sends recording and
  // summary events to (docs/events.md). Summary first, form on Edit. The test
  // button sends to the SAVED receiver, so save, then test. The secret is
  // write-only: the server only says whether one is set.
  //
  // One request at a time: every action is disabled while one is in flight,
  // so no answer can overtake another.
  import { onMount } from "svelte";
  import { around } from "cassini-viewer/ui/richText";
  import { formatCount, LL } from "cassini-viewer/i18n";
  import { copyWithFallback } from "./clipboard";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import { formatAge } from "./operator/readiness";
  import type { WebhookSettings, WebhookSettingsUpdate } from "./operator/types";
  import {
    DEFAULT_TIMEOUT_SECONDS,
    deliveryStatus,
    draftFrom,
    fieldForErrorCode,
    generateSecret,
    isAuthRefusal,
    MAX_SUMMARY_NOTICE_LENGTH,
    normalizeSummaryNotice,
    noticeHost,
    SUMMARY_COMPLETED,
    testRefusal,
    webhookUpdate,
    type WebhookDraft,
    type WebhookErrors,
    type WebhookField,
    type WebhookFieldError,
  } from "./operator/webhook";

  export let operatorClient: OperatorClient | null = null;

  const REFERENCE_URL = "https://github.com/codemyriad/gocassini/blob/main/docs/events.md";

  let settings: WebhookSettings | null = null;
  let loadError = "";
  let busy = false;

  // The open form, or null. `editing` is whether it changes the saved
  // receiver (an empty secret keeps the stored one) or sets up a new one.
  let draft: WebhookDraft | null = null;
  let editing = false;
  let errors: WebhookErrors = {};
  let formError = "";
  let generated = "";

  // Replacing the secret from the summary, without opening the whole form.
  let replaceOpen = false;
  let replaceSecret = "";
  let replaceGenerated = "";
  let replaceError = "";

  type TestResult =
    | { kind: "saved" }
    | { kind: "answered"; status: number; ms: number }
    | { kind: "no_answer"; error: string }
    | { kind: "address_not_allowed" }
    | { kind: "refused"; message: string };
  let testResult: TestResult | null = null;
  let testing = false;

  let confirmRemove = false;
  let removeError = "";
  // The generated value that was copied, or that the clipboard refused.
  let copied = "";
  let copyBlocked = "";

  onMount(async () => {
    if (!operatorClient) {
      loadError = $LL.app.settings.webhook.no_client();
      return;
    }
    try {
      settings = await operatorClient.getWebhookSettings();
    } catch (error) {
      loadError = asMessage(error);
    }
  });

  // Runs one request with every action disabled.
  async function run(task: (client: OperatorClient) => Promise<void>) {
    if (!operatorClient || busy) return;
    busy = true;
    try {
      await task(operatorClient);
    } finally {
      busy = false;
    }
  }

  async function put(client: OperatorClient, update: WebhookSettingsUpdate) {
    settings = await client.putWebhookSettings(update);
    testResult = { kind: "saved" };
  }

  function openForm() {
    editing = settings !== null && settings.url !== "";
    draft = draftFrom(settings, editing);
    errors = {};
    formError = "";
    generated = "";
    replaceOpen = false;
    confirmRemove = false;
  }

  $: newAddress = draft !== null && editing && draft.url.trim() !== settings?.url;
  $: droppedBySave = newAddress ? (settings?.delivery.waiting ?? 0) : 0;

  function clearError(field: WebhookField) {
    if (errors[field]) errors = { ...errors, [field]: undefined };
  }

  function codedError(error: unknown) {
    const code = error instanceof OperatorHttpError ? error.details.error_code : undefined;
    return typeof code === "string" ? fieldForErrorCode(code) : null;
  }

  function save() {
    if (!draft) return;
    formError = "";
    // The one check made here: the others come back from the server as codes.
    if (draft.events.length === 0) {
      errors = { events: "events" };
      return;
    }
    const update = webhookUpdate(draft, editing ? settings : null);
    void run(async (client) => {
      try {
        await put(client, update);
        draft = null;
      } catch (error) {
        const placed = codedError(error);
        if (placed) errors = { [placed.field]: placed.error };
        else formError = asMessage(error);
      }
    });
  }

  function saveSecret() {
    replaceError = "";
    void run(async (client) => {
      try {
        await put(client, { secret: replaceSecret.trim() });
        closeReplace();
      } catch (error) {
        const placed = codedError(error);
        replaceError = placed?.field === "secret" ? errorText(placed.error) : asMessage(error);
      }
    });
  }

  function closeReplace() {
    replaceOpen = false;
    replaceSecret = "";
    replaceGenerated = "";
    replaceError = "";
  }

  function sendTest() {
    testResult = null;
    testing = true;
    void run(async (client) => {
      try {
        const answer = await client.testWebhook();
        testResult =
          answer.status !== null
            ? { kind: "answered", status: answer.status, ms: answer.duration_ms }
            : answer.error_code === "webhook_address_not_allowed"
              ? { kind: "address_not_allowed" }
              : { kind: "no_answer", error: answer.error };
      } catch (error) {
        const refusal = error instanceof OperatorHttpError ? testRefusal(error.status, error.details.error_code) : null;
        const message =
          refusal === "not_configured" ? t.test_not_configured() : refusal === "gateway_timeout" ? t.test_gateway_timeout() : asMessage(error);
        testResult = { kind: "refused", message };
      }
      // Delivery may have moved on, or the receiver been removed elsewhere.
      try {
        settings = await client.getWebhookSettings();
      } catch {
        // Keep what is shown; the test result says what happened.
      }
      testing = false;
    });
  }

  function remove() {
    removeError = "";
    void run(async (client) => {
      try {
        // Everything, so nothing of the old receiver is kept for the next.
        settings = await client.putWebhookSettings({ url: "", secret: "", events: [], timeout_seconds: DEFAULT_TIMEOUT_SECONDS });
        confirmRemove = false;
        testResult = null;
      } catch (error) {
        removeError = asMessage(error);
      }
    });
  }

  function generateForForm() {
    if (!draft) return;
    generated = generateSecret();
    draft.secret = generated;
    copied = "";
    copyBlocked = "";
    clearError("secret");
  }

  function generateForReplace() {
    replaceGenerated = generateSecret();
    replaceSecret = replaceGenerated;
    replaceError = "";
    copied = "";
    copyBlocked = "";
  }

  // When the clipboard refuses (see copyWithFallback), the field is left
  // selected for the keyboard. It is found before the await.
  async function copy(value: string, event: MouseEvent) {
    const field = (event.currentTarget as HTMLElement).parentElement?.querySelector("input") ?? null;
    copyBlocked = "";
    if (await copyWithFallback(value, field)) copied = value;
    else copyBlocked = value;
  }

  function errorText(error: WebhookFieldError): string {
    return (t as unknown as Record<string, (args: { max: number }) => string>)[`error_${error}`]({ max: MAX_SUMMARY_NOTICE_LENGTH });
  }

  // Under each checkbox; an event this build has no words for shows its name
  // only. summary.completed has a bold part, so the markup draws it.
  function eventDescription(name: string): string {
    const describe = (t.event as unknown as Record<string, (() => string) | undefined>)[name.replace(".", "_")];
    return describe ? describe() : "";
  }

  function asMessage(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }

  $: t = $LL.app.settings.webhook;
  $: configured = settings !== null && settings.url !== "";
  $: catalog = settings?.catalog ?? [];
  $: postText = around(t.empty_text, "post");
  $: urlHelp = around(t.url_help, "post");
  $: reasonText = around(t.load_failed_reason, "reason");
  $: summaryCompleted = around(t.event.summary_completed, "emphasis");
  // The sentence a meeting will show (#532), from the address being typed.
  $: previewHost = draft ? noticeHost(draft.url) : "";
  $: previewReason = draft ? normalizeSummaryNotice(draft.summaryNotice) : "";
  $: previewNotice = previewHost
    ? $LL.viewer.meeting.summary_sent.notice({ host: previewHost }) + (previewReason ? ` ${previewReason}` : "")
    : "";

  // An error recorded before Cassini kept its time has none.
  function latestErrorText(words: typeof t, at: string): [string, string] {
    if (at === "") return around(words.latest_error_untimed, "error");
    return around((args: { error: string }) => words.latest_error({ when: formatAge(at), error: args.error }), "error");
  }
</script>

{#snippet generatedBox(value: string)}
  <div class="wh-gen" role="status">
    <span>{t.generated_new()}</span>
    <!-- Readonly text: the one time it shows, and what a refused clipboard selects. -->
    <input class="wh-gen-value" type="text" readonly {value} aria-label={t.generated_label()} on:focus={(event) => event.currentTarget.select()} />
    <button class="link-btn" type="button" on:click={(event) => copy(value, event)}>
      {copied === value ? $LL.common.status.copied() : t.copy()}
    </button>
    {#if copyBlocked === value}<span class="basis-full text-warning">{t.copy_blocked()}</span>{/if}
  </div>
{/snippet}

{#snippet badge(tone: string, text: string)}
  <span class="badge badge-sm badge-soft gap-1.5 font-semibold {tone}"><span class="status status-xs bg-current"></span>{text}</span>
{/snippet}

<header class="op-panel-head">
  <div>
    <h1>{t.heading()}</h1>
    <p>{t.intro()}</p>
  </div>
  <div class="op-panel-actions">
    <a class="link-btn underline" href={REFERENCE_URL} target="_blank" rel="noopener noreferrer">{t.reference()}</a>
  </div>
</header>

{#if loadError}
  <div class="err-box" role="alert">{loadError}</div>
{:else if !settings}
  <p class="op-state">{t.loading()}</p>
{:else if draft}
  <form class="wh-form" novalidate on:submit|preventDefault={save}>
    <h3 class="set-row-name">{editing ? t.form_edit() : t.form_new()}</h3>
    <div class="wh-grid2">
      <label class="op-field">
        <span class="op-field-label">{t.field_url()}</span>
        <input class="op-input" class:wh-invalid={errors.url} type="url" placeholder={t.url_placeholder()} bind:value={draft.url} on:input={() => clearError("url")} />
        <span class="wh-help">{urlHelp[0]}<code>POST</code>{urlHelp[1]}</span>
        {#if errors.url}<span class="wh-ferr">{errorText(errors.url)}</span>{/if}
      </label>
      <label class="op-field">
        <span class="op-field-label">{t.field_timeout()}</span>
        <span class="flex items-center gap-2 text-[13px]">
          <input class="op-input w-[72px]" class:wh-invalid={errors.timeout} type="number" min="1" max="60" step="1" bind:value={draft.timeoutSec} on:input={() => clearError("timeout")} />
          {t.seconds()}
        </span>
        {#if errors.timeout}<span class="wh-ferr">{errorText(errors.timeout)}</span>{/if}
      </label>
    </div>

    <div class="op-field">
      <label class="op-field-label" for="wh-secret">{t.field_secret()}</label>
      <div class="flex gap-2">
        <input
          id="wh-secret"
          class="op-input"
          class:wh-invalid={errors.secret}
          type="password"
          autocomplete="off"
          placeholder={editing && settings.secret_set && !newAddress ? t.secret_placeholder_keep() : t.secret_placeholder_new()}
          bind:value={draft.secret}
          on:input={() => clearError("secret")}
        />
        <button class="op-btn ghost" type="button" on:click={generateForForm}>{t.generate()}</button>
      </div>
      {#if generated && draft.secret === generated}{@render generatedBox(generated)}{/if}
      {#if errors.secret}<p class="wh-ferr">{errorText(errors.secret)}</p>{/if}
    </div>

    <fieldset class="op-field m-0 border-0 p-0">
      <legend class="op-field-label mb-0.5 flex w-full gap-3 p-0">
        {t.field_events()}
        <button
          class="link-btn ml-auto"
          type="button"
          on:click={() => {
            if (draft) draft.events = [...catalog];
            clearError("events");
          }}
        >
          {t.select_all()}
        </button>
      </legend>
      <p class="wh-help">{t.events_help()}</p>
      <div class="wh-checks">
        {#each catalog as name (name)}
          <div class="wh-check">
            <label>
              <input type="checkbox" value={name} bind:group={draft.events} on:change={() => clearError("events")} />
              <span>
                <span class="font-mono text-[11.5px]">{name}</span>
                {#if name === SUMMARY_COMPLETED}
                  <span class="wh-help block">{summaryCompleted[0]}<strong>{t.event.summary_completed_emphasis()}</strong>{summaryCompleted[1]}</span>
                {:else if eventDescription(name)}<span class="wh-help block">{eventDescription(name)}</span>{/if}
              </span>
            </label>
            {#if name === SUMMARY_COMPLETED && draft.events.includes(SUMMARY_COMPLETED)}
              <!-- Only while ticked; the stored reason is kept when unticked. -->
              <div class="mt-2 mb-1 ml-[21px] flex flex-col gap-1">
                <label class="op-field-label" for="wh-summary-notice">{t.field_summary_notice()}</label>
                <input
                  id="wh-summary-notice"
                  class="op-input w-full"
                  class:wh-invalid={errors.summary_notice}
                  type="text"
                  placeholder={t.summary_notice_placeholder()}
                  bind:value={draft.summaryNotice}
                  on:input={() => clearError("summary_notice")}
                />
                {#if errors.summary_notice}<p class="wh-ferr">{errorText(errors.summary_notice)}</p>{/if}
                {#if previewNotice}<p class="wh-help" data-testid="summary-notice-preview">{t.summary_notice_preview({ notice: previewNotice })}</p>{/if}
              </div>
            {/if}
          </div>
        {/each}
      </div>
      {#if errors.events}<p class="wh-ferr">{errorText(errors.events)}</p>{/if}
    </fieldset>

    {#if formError}<div class="err-box" role="alert">{formError}</div>{/if}
    {#if droppedBySave > 0}<p class="wh-hint">{t.address_change_drops_waiting({ count: droppedBySave })}</p>{/if}

    <div class="flex flex-wrap items-center gap-3 pt-1">
      <button class="op-btn" type="submit" disabled={busy}>
        {#if busy}
          <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
          {$LL.common.action.saving()}
        {:else}
          {$LL.common.action.save()}
        {/if}
      </button>
      <button class="link-btn op-cancel" type="button" disabled={busy} on:click={() => (draft = null)}>{$LL.common.action.cancel()}</button>
    </div>
  </form>
{:else if !configured}
  <!-- Off, or saved settings Cassini refused at startup: either way the way on
       is to set one up. -->
  <div class="op-empty">
    {#if settings.error}
      <p class="t">{t.load_failed_title()}</p>
      <p>{t.load_failed_text()}</p>
      <p class="text-xs">{reasonText[0]}<code>{settings.error}</code>{reasonText[1]}</p>
    {:else}
      <p class="t">{t.empty_title()}</p>
      <p>{postText[0]}<code>POST</code>{postText[1]}</p>
    {/if}
    <button class="op-btn mt-2" type="button" on:click={openForm}>{t.set_up()}</button>
  </div>
{:else}
  {@const delivery = settings.delivery}
  {@const status = deliveryStatus(delivery)}
  {@const latestError = delivery.last_error !== "" ? latestErrorText(t, delivery.last_error_at) : null}
  <div class="flex flex-col gap-[18px]">
    <section>
      <h2 class="wh-sec-title">{t.section_receiver()}</h2>
      <div class="set-card">
        <div class="set-row wh-row">
          <div class="set-row-main">
            <p class="set-row-name">{t.address()}</p>
            <p class="set-row-sub font-mono wrap-anywhere">{settings.url}</p>
          </div>
          <button class="op-btn ghost" type="button" disabled={busy} on:click={openForm}>{$LL.common.action.edit()}</button>
        </div>
        <div class="set-row wh-row">
          <div class="set-row-main">
            <p class="set-row-name">
              {t.secret()}
              {#if settings.secret_set}{@render badge("badge-success", t.secret_set())}{/if}
            </p>
            <p class="set-row-sub">{t.secret_hidden()}</p>
            {#if replaceOpen}
              <div class="mt-2.5 flex flex-col gap-2">
                <div class="flex gap-2">
                  <input
                    class="op-input"
                    class:wh-invalid={replaceError}
                    type="password"
                    autocomplete="off"
                    aria-label={t.new_secret_placeholder()}
                    placeholder={t.new_secret_placeholder()}
                    bind:value={replaceSecret}
                    on:input={() => (replaceError = "")}
                  />
                  <button class="op-btn ghost" type="button" on:click={generateForReplace}>{t.generate()}</button>
                </div>
                {#if replaceGenerated && replaceSecret === replaceGenerated}{@render generatedBox(replaceGenerated)}{/if}
                {#if replaceError}<p class="wh-ferr">{replaceError}</p>{/if}
                <div class="flex items-center gap-3">
                  <button class="op-btn" type="button" disabled={busy} on:click={saveSecret}>{t.save_secret()}</button>
                  <button class="link-btn op-cancel" type="button" on:click={closeReplace}>{$LL.common.action.cancel()}</button>
                </div>
              </div>
            {/if}
          </div>
          {#if !replaceOpen}
            <button class="link-btn" type="button" disabled={busy} on:click={() => (replaceOpen = true)}>{t.replace()}</button>
          {/if}
        </div>
        <div class="set-row wh-row">
          <div class="set-row-main">
            <p class="set-row-name">{t.events()}</p>
            <p class="set-row-sub mt-2 gap-x-2.5 gap-y-2">
              {#each settings.events as name (name)}<span class="wh-ev">{name}</span>{/each}
            </p>
          </div>
          <span class="set-row-sub font-mono">{t.timeout_fact({ seconds: formatCount(settings.timeout_seconds) })}</span>
        </div>
      </div>
    </section>

    <section>
      <h2 class="wh-sec-title">{t.section_delivery()}</h2>
      <div class="op-tint flex flex-col gap-3 px-4 py-3.5">
        <div class="flex flex-wrap items-center gap-x-3.5 gap-y-2">
          {#if status === "failing"}
            {@render badge("badge-error", t.status_failing())}
          {:else if status === "delivering"}
            {@render badge("badge-success", t.status_delivering())}
          {:else}
            {@render badge("badge-ghost", t.status_idle())}
          {/if}
          <span class="set-row-sub m-0">
            {delivery.last_delivered_at ? t.last_delivered({ when: formatAge(delivery.last_delivered_at) }) : t.never_delivered()}
          </span>
        </div>
        <div class="wh-facts">
          <div class="wh-fact">
            <div class="wh-help">{t.fact_delivered()}</div>
            <div class="wh-v">{formatCount(delivery.delivered)} <small>{t.last_7_days()}</small></div>
          </div>
          <div class="wh-fact" class:wh-bad={status === "failing"}>
            <div class="wh-help">{t.fact_waiting()}</div>
            <div class="wh-v">{formatCount(delivery.waiting)} {#if status === "failing"}<small>{t.retrying()}</small>{/if}</div>
          </div>
          <div class="wh-fact" class:wh-bad={delivery.failed > 0}>
            <div class="wh-help">{t.fact_failed()}</div>
            <div class="wh-v">{formatCount(delivery.failed)} <small>{t.last_7_days()}</small></div>
          </div>
        </div>
        {#if status === "failing" && latestError}
          <p class="wh-help wrap-anywhere">{latestError[0]}<code class="text-error">{delivery.last_error}</code>{latestError[1]}</p>
          {#if isAuthRefusal(delivery.last_error)}<p class="wh-hint">{t.auth_hint()}</p>{/if}
        {/if}
        <div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-dashed border-base-300 pt-2.5">
          <button class="op-btn ghost" type="button" disabled={busy} on:click={sendTest}>{t.send_test()}</button>
          <span class="wh-help" role="status">
            {#if testing}
              {t.sending_test()}
            {:else if testResult?.kind === "saved"}
              {t.saved_hint()}
            {:else if testResult?.kind === "answered"}
              {@const ok = testResult.status >= 200 && testResult.status <= 299}
              <span class={ok ? "text-success" : "text-error"}>
                {t.test_answered({ status: String(testResult.status), ms: formatCount(testResult.ms) })}
                {#if isAuthRefusal(`HTTP ${testResult.status}`)}{t.test_auth_refused()}{/if}
              </span>
            {:else if testResult?.kind === "no_answer"}
              <span class="text-error">{t.test_no_answer({ error: testResult.error })}</span>
            {:else if testResult?.kind === "address_not_allowed"}
              <span class="text-error">{t.test_address_not_allowed()}</span>
            {:else if testResult?.kind === "refused"}
              <span class="text-error">{testResult.message}</span>
            {/if}
          </span>
        </div>
      </div>
    </section>

    <div>
      {#if confirmRemove}
        <!-- Inline, not window.confirm(): the app runs inside a shadow root. -->
        <div class="wh-confirm" role="alertdialog" aria-label={t.remove()}>
          <span>{t.remove_confirm_text()}</span>
          <button class="op-btn wh-danger" type="button" disabled={busy} on:click={remove}>{t.remove_confirm()}</button>
          <button class="link-btn" type="button" disabled={busy} on:click={() => (confirmRemove = false)}>{t.keep()}</button>
        </div>
      {:else}
        <button class="link-btn danger" type="button" disabled={busy} on:click={() => (confirmRemove = true)}>{t.remove()}</button>
      {/if}
      {#if removeError}<div class="err-box mt-2" role="alert">{removeError}</div>{/if}
    </div>
  </div>
{/if}

<style>
  .wh-sec-title {
    margin: 4px 0 6px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  .wh-help {
    margin: 0;
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .wh-ferr {
    margin: 0;
    font-size: 12px;
    font-weight: 550;
    color: var(--color-error);
  }
  .op-input.wh-invalid {
    border-color: var(--color-error);
  }
  .wh-hint {
    margin: 0;
    padding-left: 10px;
    font-size: 12.5px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
    border-left: 2px solid var(--color-base-300);
  }
  .wh-ev {
    padding: 2px 8px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--color-base-content);
    background-color: var(--op-code-bg);
    box-shadow: inset 0 0 0 1px var(--op-code-border);
    border-radius: 5px;
  }
  .wh-facts {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px;
  }
  .wh-fact {
    padding: 8px 10px;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: 8px;
  }
  .wh-v {
    font-size: 15px;
    font-weight: 650;
    font-variant-numeric: tabular-nums;
  }
  .wh-v small {
    font-size: 12px;
    font-weight: 500;
    opacity: 0.65;
  }
  .wh-bad .wh-v {
    color: var(--color-error);
  }
  .wh-confirm {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
    padding: 10px 12px;
    font-size: 12.5px;
    border: 1px solid color-mix(in oklch, var(--color-error) 35%, var(--color-base-300));
    border-radius: var(--radius-box, 0.5rem);
  }
  .op-btn.wh-danger {
    color: var(--color-error-content);
    background-color: var(--color-error);
  }
  .op-btn.ghost {
    color: var(--color-base-content);
    background-color: var(--color-base-200);
    box-shadow: inset 0 0 0 1px var(--color-base-300);
    white-space: nowrap;
  }
  .wh-form {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 14px 16px 16px;
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 0.5rem);
  }
  .wh-grid2 {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 160px;
    gap: 14px;
  }
  .wh-gen {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 10px;
    margin-top: 4px;
    padding: 7px 10px;
    font-size: 12px;
    background-color: var(--color-base-200);
    border: 1px dashed var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
  }
  .wh-gen-value {
    flex: 1 1 24ch;
    min-width: 0;
    font-family: var(--font-mono);
    background: transparent;
    border: 0;
    outline: none;
  }
  .wh-checks {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px 18px;
    padding-top: 6px;
  }
  .wh-check > label {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    cursor: pointer;
  }
  .wh-check > label input {
    margin-top: 3px;
  }
  @media (max-width: 560px) {
    .wh-grid2,
    .wh-facts,
    .wh-checks {
      grid-template-columns: minmax(0, 1fr);
    }
    .wh-row {
      flex-wrap: wrap;
    }
  }
</style>
