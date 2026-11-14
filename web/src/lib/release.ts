// Where the version at the foot of the menu links (R77, owner 2026-11-15): a
// release's own page on GitHub, or the list of releases for a build that isn't
// one (0.0.0-dev, 1.9.1-dev+abc1234). A fork changes RELEASES, and only it.
export const RELEASES = 'https://github.com/RIKIKU/PagerArchive/releases';

/** A release version is MAJOR.MINOR.PATCH and nothing else (12.1). */
export const releaseHref = (version: string) => (/^\d+\.\d+\.\d+$/.test(version) ? `${RELEASES}/tag/v${version}` : RELEASES);
