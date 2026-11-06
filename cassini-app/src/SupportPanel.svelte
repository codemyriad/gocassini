<script lang="ts">
  import { ExternalLink } from "@lucide/svelte";
  import { SETUP_CALL_URL, SUPPORT_URL } from "./operator/support";
</script>

<!-- The first version of the Support Cassini page. It asks once, here, where
     only an administrator looks: no banner, no prompt, nothing locked. The
     supporter licence (measured usage, a key, a badge) replaces this body at
     the same panel id later. -->
<header class="op-panel-head">
  <div>
    <div class="op-panel-title">
      <h1>Support Cassini</h1>
    </div>
    <p>Cassini is built by Codemyriad, a small, independent team. Here's how you can help the project, and how we can help you.</p>
  </div>
</header>

<section class="op-tint support-card" aria-labelledby="support-heading">
  <h2 id="support-heading" class="op-card-title">Fund its development</h2>
  <p>
    Cassini is free software, and every feature will stay free. If it's useful to you,
    you can support its development with any amount from €2. It unlocks no features, just some
    good karma.
  </p>
  <div>
    <a class="op-btn inline-flex items-center gap-1.5" href={SUPPORT_URL} target="_blank" rel="noreferrer">
      Support Cassini<ExternalLink size={14} aria-hidden="true" />
    </a>
  </div>
  <p class="support-fine">Payment is handled by Stripe and goes to Codemyriad, the team that builds Cassini.</p>
</section>

<section class="op-tint support-card" aria-labelledby="setup-heading">
  <h2 id="setup-heading" class="op-card-title">Running Cassini for an organisation?</h2>
  <p>We help with setup and support. Book a 30-minute call to talk it through.</p>
  <div>
    <a class="op-btn inline-flex items-center gap-1.5" href={SETUP_CALL_URL} target="_blank" rel="noreferrer">
      Book a call<ExternalLink size={14} aria-hidden="true" />
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
