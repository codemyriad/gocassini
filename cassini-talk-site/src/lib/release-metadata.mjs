// Shared by the static build and browser refresh. Read only the release fields;
// never modify the signed appcast or download URLs owned by the Mac publisher.
export function parseMacRelease(xml) {
  const releases = [...xml.matchAll(/<item\b[^>]*>([\s\S]*?)<\/item>/g)].map(([, item]) => {
    const text = tag => item.match(new RegExp(`<${tag}\\b[^>]*>([\\s\\S]*?)</${tag}>`))?.[1]?.trim();
    const version = text('sparkle:shortVersionString');
    const build = Number(text('sparkle:version'));
    const publishedAt = text('pubDate');
    const size = Number(item.match(/<enclosure\b[^>]*\blength=["'](\d+)["']/)?.[1]);
    return { version, build, publishedAt, size };
  }).filter(release => /^\d+\.\d+\.\d+$/.test(release.version ?? '') &&
    Number.isFinite(Date.parse(release.publishedAt)) && release.size > 0);
  releases.sort((a, b) => (b.build - a.build) || (Date.parse(b.publishedAt) - Date.parse(a.publishedAt)));
  if (!releases.length) throw new Error('The Mac appcast contains no valid release');
  return releases[0];
}

export function parseMirroredRelease(release, platform) {
  if (release.schema !== 1 || release.platform !== platform || !/^\d+\.\d+\.\d+$/.test(release.version)) {
    throw new Error(`Invalid ${platform} release metadata`);
  }
  // Flatpak is no longer offered. Older manifests can still contain its history.
  const primary = platform === 'linux'
    ? ['ubuntu', 'debian', 'fedora', 'opensuse', 'arch'].map(kind => release.assets?.find(asset => asset.kind === kind)).find(Boolean)
    : release.assets?.find(asset => asset.kind === release.primary);
  if (!primary || !Number.isFinite(Date.parse(release.published_at))) throw new Error(`No supported ${platform} installer`);
  return { version: release.version, publishedAt: release.published_at, size: primary.size };
}

export function releaseDate(value) {
  return new Date(value).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' });
}

export function releaseLine(release, note) {
  return [`Version ${release.version}`, releaseDate(release.publishedAt), `${(release.size / 1_000_000).toFixed(1)} MB`, note].filter(Boolean).join(' · ');
}

export async function loadReleases(apps) {
  const result = new Map();
  await Promise.all(['mac', 'android', 'linux'].map(async platform => {
    const endpoint = platform === 'mac' ? 'appcast.xml' : 'release.json';
    const response = await fetch(`https://cassini.talk/download/${platform}/${endpoint}`, {
      cache: 'no-cache', headers: { 'User-Agent': 'Cassini-release-verifier/1' }, signal: AbortSignal.timeout(30000),
    });
    if (!response.ok) throw new Error(`Cannot build current ${platform} release: HTTP ${response.status}`);
    const release = platform === 'mac' ? parseMacRelease(await response.text()) : parseMirroredRelease(await response.json(), platform);
    result.set(platform, releaseLine(release, apps.find(app => app.id === platform)?.note));
  }));
  return result;
}
