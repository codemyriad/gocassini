export interface DemoMeeting {
  id: string;
  path: string;
  title: string;
  dateLabel: string;
  speakerCount: number;
  segmentCount: number;
  digestDurationMs: number;
  speakers: string[];
  teaser: string;
}

// The publish-safe branch intentionally ships without bundled meeting demos.
export const DEMO_MEETINGS: DemoMeeting[] = [];
