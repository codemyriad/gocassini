<script lang="ts">
  // The optional speaker separation model, as its own Settings card: people
  // run it from a meeting's participant list, so it is not a step of what
  // happens after each meeting (docs/speaker-separation.md).
  import type { OperatorClient } from "./operator/client";
  import type { SpeechModelInventory } from "./operator/types";
  import OptionalModelCard from "./OptionalModelCard.svelte";
  import { voiceSeparationModel } from "./speechModels";
  export let client: OperatorClient | null = null;
  export let inventory: SpeechModelInventory | null = null;
</script>

<OptionalModelCard {client} {inventory} model={voiceSeparationModel(inventory)} title="Voice separation" headingId="voice-separation-heading">
  For Cassini to tell apart several people talking on one device in a call, download this model. Anyone who can read the meeting can then split that device's audio into separate voices from its participant list. It runs on this server's CPU, only when someone asks.
</OptionalModelCard>
