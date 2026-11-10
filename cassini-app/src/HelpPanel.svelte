<script lang="ts">
  import SupportBundle from "./SupportBundle.svelte";
</script>

<!-- Operator › Help (D-910): the installation's support bundle. It used to sit
     at the top of Doctor; Doctor is for reading checks, and this is the one
     place an administrator goes to send something to whoever supports them. -->
<header class="op-panel-head">
  <div>
    <div class="op-panel-title">
      <h1>Help</h1>
    </div>
    <p>Recording failing, or a meeting not showing up? Download a support bundle and attach it to your support request.</p>
  </div>
</header>

<section class="op-tint help-card" aria-labelledby="help-bundle-heading">
  <h2 id="help-bundle-heading">Support bundle</h2>
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
