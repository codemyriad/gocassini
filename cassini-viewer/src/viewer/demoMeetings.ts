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

export const DEMO_MEETINGS: DemoMeeting[] = [
  {
    id: "daily-meeting--2026-03-04--12-36-53",
    path: "/demo/daily-meeting--2026-03-04--12-36-53",
    title: "Daily meeting",
    dateLabel: "March 4, 2026 at 12:36",
    speakerCount: 5,
    segmentCount: 244,
    digestDurationMs: 1611880,
    speakers: ["Alex", "Chris", "Ivan", "Silvio", "chima"],
    teaser:
      "Because I see what you're saying, but I disagree with some of it. And from the perspective, I feel like you've taken the technical approach",
  },
  {
    id: "daily-meeting--2026-03-05--12-38-29",
    path: "/demo/daily-meeting--2026-03-05--12-38-29",
    title: "Daily meeting",
    dateLabel: "March 5, 2026 at 12:38",
    speakerCount: 3,
    segmentCount: 139,
    digestDurationMs: 857937,
    speakers: ["Chris", "Ivan", "Silvio"],
    teaser:
      "Storybook add-on for Vtest. And I needed to run rush update. Once I ran rush update, what happened is I get these crypti",
  },
  {
    id: "daily-meeting--2026-03-06--12-35-03",
    path: "/demo/daily-meeting--2026-03-06--12-35-03",
    title: "Daily meeting",
    dateLabel: "March 6, 2026 at 12:35",
    speakerCount: 4,
    segmentCount: 190,
    digestDurationMs: 1045100,
    speakers: ["Alex", "Chris", "Silvio", "chima"],
    teaser:
      "Yeah, so sorry for being late to the meeting. I will need to change my laptop at some point",
  },
];
