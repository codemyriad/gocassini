<script lang="ts">
  import { LL } from "cassini-viewer/i18n";
  import SupportBundle from "./SupportBundle.svelte";
</script>

<!-- Operator › Help (D-910): the installation's support bundle. It used to sit
     at the top of Doctor; Doctor is for reading checks, and this is the one
     place an administrator goes to send something to whoever supports them. -->
<header class="op-panel-head">
  <div>
    <div class="op-panel-title">
      <h1>{$LL.app.operator.help_panel.title()}</h1>
    </div>
    <p>{$LL.app.operator.help_panel.intro()}</p>
  </div>
</header>

<section class="op-tint help-card" aria-labelledby="help-bundle-heading">
  <h2 id="help-bundle-heading">{$LL.app.operator.help_panel.bundle_heading()}</h2>
  <SupportBundle administrator={true} surface="operator" />
</section>

<style>
  .help-card {
    display: grid;
    gap: 10px;
    max-width: 640px;
    padding: 16px;
  }
  .help-card h2 {
    margin: 0;
    font-size: 14px;
    font-weight: 600;
  }
</style>
