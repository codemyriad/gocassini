/**
 * The help centre. A guide with a `slug` has an article in src/content/help;
 * the rest are titles from the mocks still waiting to be written (open follow-up).
 */
export interface Guide {
  label: string;
  slug?: string;
}

export interface Category {
  id: string;
  title: string;
  /** Inner markup of a 24 × 24 Lucide icon. */
  icon: string;
  guides: Guide[];
}

export const categories: Category[] = [
  {
    id: "getting-started",
    title: "Getting started",
    icon: "<circle cx=\"12\" cy=\"12\" r=\"10\"></circle><path d=\"m16.24 7.76-1.804 5.411a2 2 0 0 1-1.265 1.265L7.76 16.24l1.804-5.411a2 2 0 0 1 1.265-1.265z\"></path>",
    guides: [
      { label: "Install cassini.talk" },
      { label: "Make your first recording" },
      { label: "Download the speech model" },
      { label: "Allow microphone access" },
    ],
  },
  {
    id: "recording",
    title: "Recording",
    icon: "<path d=\"M12 19v3\"></path><path d=\"M19 10v2a7 7 0 0 1-14 0v-2\"></path><rect x=\"9\" y=\"2\" width=\"6\" height=\"13\" rx=\"3\"></rect>",
    guides: [
      { label: "Pause, resume and mark a moment" },
      { label: "Record a call on your computer" },
      { label: "Keep recording with the screen locked" },
      { label: "Import audio from another app" },
    ],
  },
  {
    id: "transcription",
    title: "Transcription",
    icon: "<path d=\"M15 12h-5\"></path><path d=\"M15 8h-5\"></path><path d=\"M19 17V5a2 2 0 0 0-2-2H4\"></path><path d=\"M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3\"></path>",
    guides: [
      { label: "Transcribe on your device", slug: "transcribe-on-your-device" },
      { label: "Choose the speech language" },
      { label: "Stop and keep progress" },
      { label: "Transcribe a recording again" },
    ],
  },
  {
    id: "speakers-and-voices",
    title: "Speakers and voices",
    icon: "<path d=\"M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2\"></path><circle cx=\"9\" cy=\"7\" r=\"4\"></circle><path d=\"M22 21v-2a4 4 0 0 0-3-3.87\"></path><path d=\"M16 3.13a4 4 0 0 1 0 7.75\"></path>",
    guides: [
      { label: "Name a speaker" },
      { label: "Merge two speakers" },
      { label: "Save a voice for next time" },
      { label: "Forget saved voices" },
    ],
  },
  {
    id: "files-and-sharing",
    title: "Files and sharing",
    icon: "<path d=\"M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z\"></path><path d=\"M14 2v4a2 2 0 0 0 2 2h4\"></path>",
    guides: [
      { label: "Export text or subtitles" },
      { label: "Share a Cassini .opus file" },
      { label: "Open a file someone sent you" },
      { label: "Edit a passage and go back" },
    ],
  },
  {
    id: "privacy-and-storage",
    title: "Privacy and storage",
    icon: "<rect width=\"18\" height=\"11\" x=\"3\" y=\"11\" rx=\"2\" ry=\"2\"></rect><path d=\"M7 11V7a5 5 0 0 1 10 0v4\"></path>",
    guides: [
      { label: "What stays on your device" },
      { label: "Recently Deleted" },
      { label: "Free up space" },
      { label: "Erase all voice data" },
    ],
  },
];

/** The article sidebar, in reading order: prev and next follow it. */
export const sidebar: { section: string; guides: Guide[] }[] = [
  { section: "Getting started", guides: [
    { label: "Install cassini.talk" },
    { label: "Make your first recording" },
    { label: "Download the speech model" },
  ] },
  { section: "Transcription", guides: [
    { label: "Transcribe on your device", slug: "transcribe-on-your-device" },
    { label: "Choose the speech language" },
    { label: "Stop and keep progress" },
    { label: "Transcribe a recording again" },
  ] },
  { section: "Speakers and voices", guides: [
    { label: "Name a speaker" },
    { label: "Save a voice for next time" },
  ] },
  { section: "Privacy and storage", guides: [
    { label: "What stays on your device" },
    { label: "Recently Deleted" },
  ] },
];

export const platforms = [
  { id: "ios", label: "iPhone and iPad" },
  { id: "android", label: "Android" },
  { id: "mac", label: "Mac" },
  { id: "windows", label: "Windows" },
  { id: "linux", label: "Linux" },
] as const;
