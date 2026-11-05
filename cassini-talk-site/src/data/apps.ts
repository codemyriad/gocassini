import type { Glyph } from "./glyphs";

/**
 * Marks a fact that is not final yet: store links that are still "#", the
 * Windows requirement, version and date, and the ink store buttons that stand
 * in for the official badges. Search for PLACEHOLDER before launch.
 */
export const PLACEHOLDER = "#";

export interface StoreButton {
  label: string;
  href: string;
  /** A brand glyph, or "download" for the plain download arrow. */
  icon: Glyph | "download";
  /** The official store badge still has to replace this ink button. */
  badgePending?: boolean;
}

export interface App {
  /** Anchor on /download, linked from Home's platform tiles. */
  id: "ios" | "android" | "mac" | "windows" | "linux";
  name: string;
  glyph: Glyph;
  glyphSize: number;
  requirement: string;
  blurb: string;
  button: StoreButton;
  /** Other ways to get it, after the main button. */
  links?: { label: string; href: string }[];
  /** "Or download the APK →" reads as one link; .deb/.rpm/Arch as a group. */
  linksStyle?: "inline" | "group";
  version: string;
  date?: string;
  note?: string;
  /** Half width on the top row, or a third on the row below. */
  width: "half" | "third";
  /** Facts in this card are not confirmed yet. */
  placeholder?: boolean;
  source: string;
  /** Home's platform tile caption. */
  tileCaption: string;
}

export const apps: App[] = [
  {
    id: "ios",
    name: "iPhone and iPad",
    glyph: "apple",
    glyphSize: 22,
    requirement: "iOS 26 or iPadOS 26 or later",
    blurb: "Pause and finish from the Lock Screen, read beside your library in split view on iPad, and keep recording with the screen off.",
    button: { label: "App Store (coming soon)", href: PLACEHOLDER, icon: "apple", badgePending: true },
    version: "0.1.0",
    note: "speech model about 531 MB",
    width: "half",
    source: "https://github.com/codemyriad/cassini-ios",
    tileCaption: "App Store",
  },
  {
    id: "android",
    name: "Android",
    glyph: "android",
    glyphSize: 22,
    requirement: "Android 8.0 or later · 64-bit ARM",
    blurb: "Read a live draft while it transcribes, with the speakers shown as they appear. Stop at any point and pick up from the last checkpoint.",
    button: { label: "Download the APK", href: "https://cassini.talk/download/android/cassini-android.apk", icon: "download" },
    links: [{ label: "Google Play (coming soon)", href: PLACEHOLDER }],
    linksStyle: "inline",
    version: "0.4.0",
    date: "5 Nov 2026",
    note: "speech model about 530 MB",
    width: "half",
    source: "https://github.com/codemyriad/cassini-android",
    tileCaption: "Google Play or APK",
  },
  {
    id: "mac",
    name: "Mac",
    glyph: "apple",
    glyphSize: 22,
    requirement: "macOS 15 or later · Apple silicon and Intel",
    blurb: "A timer in the menu bar while you record, and Quick Look for any .opus file in Finder.",
    button: { label: "Download for Mac", href: "https://cassini.talk/download/mac/Cassini-talk.dmg", icon: "download" },
    version: "",
    note: "Whistle speech model included",
    links: [{ label: "Latest release details →", href: "https://cassini.talk/download/mac/" }],
    width: "third",
    source: "https://github.com/codemyriad/cassini-mac",
    tileCaption: "Download",
  },
  {
    id: "windows",
    name: "Windows",
    glyph: "windows",
    glyphSize: 18,
    // PLACEHOLDER: requirement, version, date, download and source are not known yet,
    // nor whether Windows ships at launch.
    requirement: "Coming soon",
    blurb: "Record your microphone and the others, and calls in Chrome, Edge and Brave with each person kept apart.",
    button: { label: "Coming soon", href: PLACEHOLDER, icon: "download" },
    version: "",
    width: "third",
    placeholder: true,
    source: PLACEHOLDER,
    tileCaption: "Download",
  },
  {
    id: "linux",
    name: "Linux",
    glyph: "linux",
    glyphSize: 22,
    requirement: "GNOME · 64-bit Intel and AMD",
    blurb: "Native packages for Ubuntu, Debian, Fedora, openSUSE and Arch. Requires GTK 4.18 and libadwaita 1.8.",
    button: { label: "Download for Ubuntu", href: "https://cassini.talk/download/linux/cassini-viewer-ubuntu-amd64.deb", icon: "download" },
    links: [
      { label: "Debian testing (.deb)", href: "https://cassini.talk/download/linux/cassini-viewer-debian-amd64.deb" },
      { label: "Fedora 44 (.rpm)", href: "https://cassini.talk/download/linux/cassini-viewer-fedora-x86_64.rpm" },
      { label: "openSUSE Tumbleweed (.rpm)", href: "https://cassini.talk/download/linux/cassini-viewer-opensuse-x86_64.rpm" },
      { label: "Arch", href: "https://cassini.talk/download/linux/cassini-viewer-x86_64.pkg.tar.zst" },
    ],
    linksStyle: "group",
    version: "0.3.0",
    date: "29 Oct 2026",
    width: "third",
    source: "https://github.com/codemyriad/cassini-gnome",
    tileCaption: ".deb, .rpm, Arch",
  },
];

export const metaLine = (app: App) => [app.version && "Version " + app.version, app.date, app.note].filter(Boolean).join(" · ");
