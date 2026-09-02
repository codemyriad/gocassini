type SelectedJobIdentity = {
  job: {
    id: string;
  };
};

export function shouldShowDetailLoading(
  loadingDetail: boolean,
  selectedJob: SelectedJobIdentity | null,
  selectedJobId: string,
): boolean {
  return loadingDetail && (!selectedJob || selectedJob.job.id !== selectedJobId);
}

/** The parts of a job the panel needs to label it. Structural on purpose, so a
 * test can name a case without building a whole Job. */
type LabelledJob = {
  request_json: string;
  room_name?: string | null;
};

export function requestUrlLabel(requestJSON: string): string {
  try {
    const payload = JSON.parse(requestJSON) as { url?: unknown };
    return typeof payload.url === "string" && payload.url.trim() !== "" ? payload.url : "\u2014";
  } catch {
    return "\u2014";
  }
}

/**
 * Names a job after the conversation it recorded.
 *
 * Falls back to the token in the request URL for a non-Talk job, for a job
 * whose room-name lookup never completed, and for every job recorded before
 * the operator promoted the room to a column (D-646) — that fallback names a
 * row after an opaque string that tells an operator nothing about which
 * conversation it was, which is exactly what the room name replaces.
 */
export function meetingLabel(job: LabelledJob): string {
  const room = job.room_name?.trim();
  if (room) {
    return room;
  }
  const url = requestUrlLabel(job.request_json);
  if (url === "\u2014") {
    return "Recording";
  }
  try {
    const token = new URL(url).pathname.split("/").filter(Boolean).pop();
    return token ? `Call ${token}` : url;
  } catch {
    return url;
  }
}
