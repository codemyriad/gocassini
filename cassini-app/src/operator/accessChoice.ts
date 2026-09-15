import { writable } from "svelte/store";
import type { AccessMode } from "./recordingAccess";

// The audience an administrator picked in the first-run dialog when it is not
// the one in force. The dialog cannot run the switch itself (apps to install,
// a confirmation, a move), so it hands the choice to "Who can see recordings",
// which opens its own switch flow for it and clears this.
export const pendingAccessChoice = writable<AccessMode | null>(null);
