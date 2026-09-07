### Fixed
- Build: the ExApp image installs the web workspace from the root `package-lock.json` with `npm ci` instead of resolving it afresh from the registry on every build. On 2026-09-03 the fresh resolution tripped an npm 10.9 bug (`Cannot read properties of null (reading 'edgesOut')`) on trees that had built minutes earlier.
