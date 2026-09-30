import { get, writable } from "svelte/store";
import type { AnnotationResult, MeetingAnnotations } from "../../viewer/annotations";
import { createMarksSession, type ApplyAnnotations, type LoadAnnotations, type MarksSession } from "./session";

interface AnnotationNotice { meetingId: string; title: string; message: string; retryable: boolean }

// Owned by the browse shell, not the disposable meeting overlay. Each meeting
// retains its document, pending intents and uncertain receipt across navigation.
export function createMeetingMarksSessions(
  schedule: (write: () => Promise<void>) => Promise<void>,
  changed: (result: AnnotationResult) => void,
) {
  const sessions = new Map<string, { title: string; session: MarksSession; unsubscribe: () => void }>();
  const state = writable<AnnotationNotice[]>([]);
  let active: MarksSession | null = null;
  function publish() {
    state.set([...sessions].flatMap(([meetingId, { title, session }]) => {
      const value = get(session);
      return value.error ? [{ meetingId, title, message: value.error, retryable: value.retryable }] : [];
    }));
  }
  return {
    subscribe: state.subscribe,
    activate(meetingId: string, title: string, load: LoadAnnotations | null, apply: ApplyAnnotations | null) {
      active?.suspend();
      active = null;
      if (!meetingId || !load) return null;
      let entry = sessions.get(meetingId);
      if (!entry) {
        const session = createMarksSession(changed, { schedule });
        entry = { title, session, unsubscribe: () => {} };
        sessions.set(meetingId, entry);
        entry.unsubscribe = session.subscribe(publish);
        void session.open(load, apply);
      } else {
        entry.title = title;
        void entry.session.resume();
      }
      active = entry.session;
      return active;
    },
    receive(result: MeetingAnnotations) { sessions.get(result.meetingId)?.session.receive(result); },
    retry(meetingId: string) { return sessions.get(meetingId)?.session.retryWrite() ?? false; },
    dismiss(meetingId: string) { sessions.get(meetingId)?.session.dismissError(); },
    stop() {
      for (const { session, unsubscribe } of sessions.values()) { session.suspend(); unsubscribe(); }
    },
  };
}
