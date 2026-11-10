<script lang="ts">
  import { ExternalLink } from "@lucide/svelte";
  import { LL } from "cassini-viewer/i18n";
  import { SETUP_CALL_URL, SUPPORT_URL } from "./operator/support";
</script>

<!-- The first version of the Support Cassini page. It asks once, here, where
     only an administrator looks: no banner, no prompt, nothing locked. The
     supporter licence (measured usage, a key, a badge) replaces this body at
     the same panel id later. -->
<header class="op-panel-head">
  <div>
    <div class="op-panel-title">
      <h1>{$LL.app.operator.support_panel.title()}</h1>
    </div>
    <p>{$LL.app.operator.support_panel.intro()}</p>
  </div>
</header>

<section class="op-tint support-card" aria-labelledby="support-heading">
  <h2 id="support-heading" class="op-card-title">{$LL.app.operator.support_panel.fund_heading()}</h2>
  <p>{$LL.app.operator.support_panel.fund_body()}</p>
  <div>
    <a class="op-btn inline-flex items-center gap-1.5" href={SUPPORT_URL} target="_blank" rel="noreferrer">
      {$LL.app.operator.support_panel.fund_button()}<ExternalLink size={14} aria-hidden="true" />
    </a>
  </div>
  <p class="support-fine">{$LL.app.operator.support_panel.fund_fine_print()}</p>
</section>

<section class="op-tint support-card" aria-labelledby="setup-heading">
  <h2 id="setup-heading" class="op-card-title">{$LL.app.operator.support_panel.setup_heading()}</h2>
  <p>{$LL.app.operator.support_panel.setup_body()}</p>
  <div>
    <a class="op-btn inline-flex items-center gap-1.5" href={SETUP_CALL_URL} target="_blank" rel="noreferrer">
      {$LL.app.operator.support_panel.setup_button()}<ExternalLink size={14} aria-hidden="true" />
    </a>
  </div>
</section>

<style>
  .support-card {
    display: grid;
    gap: 10px;
    max-width: 640px;
    padding: 16px;
  }
  .support-card h2 {
    margin: 0;
    font-size: 14px;
    font-weight: 600;
  }
  .support-card p {
    margin: 0;
    font-size: 13px;
    line-height: 1.5;
  }
  .support-fine {
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
    font-size: 12px !important;
  }
</style>
