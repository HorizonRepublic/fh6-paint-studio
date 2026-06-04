# Contributing

## Commits and the changelog

The release `CHANGELOG.md` is generated from commit messages by
[release-please](https://github.com/googleapis/release-please), so the commit log
is the changelog source. Two rules follow from that.

**Use Conventional Commits.** Prefix every commit with a type: `feat`, `fix`,
`perf`, `refactor`, `docs`, `test`, `chore`, `ci`, `build`, `style`. An optional
scope goes in parentheses, e.g. `feat(studio): ...`.

**Only `feat`, `perf` and `fix` reach the changelog** (under Features,
Performance and Bug Fixes). Everything else is hidden, so internal work never
shows up for users and you don't have to scrub it out by hand. The mapping lives
in `release-please-config.json` under `changelog-sections`.

The commit subject becomes the changelog line verbatim, so write the subject of a
`feat`/`perf`/`fix` for a user, not for yourself:

    perf: faster generation with moment-seeded candidate search
    feat: Gaussian glow mode for smooth and gradient images

not `perf(engine): hand off to the random search at progress 0.55`.

## Pull requests

**Squash-merge** every PR. Work-in-progress commits on a branch (many small
`feat`/`refactor` steps) collapse into one commit on `main`, so they never
clutter the changelog. The squash subject is the changelog entry.

To list several changelog entries from one PR, write them as conventional-commit
lines in the squash commit body; release-please reads each one:

    feat: Gaussian glow mode for smooth and gradient images
    perf: faster generation with moment-seeded candidate search

## Releases

release-please keeps an open "release PR" that accumulates the changelog and the
version bump. Merging it tags the release and builds the Windows artifacts
(`release.yml` calling `release-build.yml`). `.release-please-manifest.json`
holds the last released version; release-please manages it, so don't edit it by
hand except to bootstrap a corrected baseline.
