<script lang="ts">
  // The optional topic suggestions model (docs/proposals/automatic-tagging).
  // This card only installs and checks it; the switch that turns suggestions
  // on comes with the step that runs after each meeting.
  import type { OperatorClient } from "./operator/client";
  import type { SpeechModelInventory } from "./operator/types";
  import OptionalModelCard from "./OptionalModelCard.svelte";
  import { topicSuggestionsModel } from "./speechModels";
  export let client: OperatorClient | null = null;
  export let inventory: SpeechModelInventory | null = null;
</script>

<OptionalModelCard {client} {inventory} model={topicSuggestionsModel(inventory)} title="Topic suggestions" headingId="topic-suggestions-heading">
  For Cassini to suggest topics for meetings from their transcripts, download this model. It reads transcripts on this server's CPU; no text leaves the server. Downloading it does not switch suggestions on.
</OptionalModelCard>
