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
 * The fallback applies to a non-Talk job, a job whose room-name lookup never
 * completed, and every job recorded before the operator promoted the room to a
 * column (D-646). It is worse than it looks: a Talk-triggered job carries
 * `baseURL` and `roomToken` rather than a `url`, so it falls all the way
 * through to the generic "Recording" — the `Call <token>` arm only reaches
 * jobs created with an explicit URL. Neither says which conversation it was,
 * which is what the room name replaces. See D-711 for the fallback itself.
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
