import { get, writable } from "svelte/store";
import { OperatorHttpError, type OperatorClient } from "./client";
import type { RetentionSettings } from "./retention";

// Coordinate local writes without retaining policy or acknowledgement state.
// Cross-session concurrency remains the API's If-Match responsibility.
export const retentionMutationBusy = writable(false);

export async function withRetentionMutation<T>(write: () => Promise<T>): Promise<T> {
  if (get(retentionMutationBusy)) throw new Error("Retention settings are being saved. Try again.");
  retentionMutationBusy.set(true);
  try { return await write(); }
  finally { retentionMutationBusy.set(false); }
}

export function dismissRetentionReminder(client: Pick<OperatorClient, "getRetention" | "putRetention">): Promise<RetentionSettings> {
  return withRetentionMutation(async () => {
    const current = await client.getRetention();
    if (current.revision > 0) return current;
    try {
      // This intentionally acknowledges the current configuration for everyone.
      // Never substitute hard-coded defaults or a potentially stale UI draft.
      return await client.putRetention(current);
    } catch (error) {
      if (!(error instanceof OperatorHttpError) || error.status !== 412) throw error;
      // Someone saved first. Read their decision; never replay our stale write.
      const latest = await client.getRetention();
      if (latest.revision > 0) return latest;
      throw error;
    }
  });
}
