# 0011. Release channels: stable, testing and dev, told apart by the tag

Status: accepted, 2026-11-06.

## Context

Every release so far is one stable tag `vX.Y.Z`, and every panel in the world offers it within ten minutes. The maintainer
works on `main` and wants to put new work in front of his own panels and willing testers first, without shipping it to
every owner at once. Owners who install a panel themselves should pick how early they want changes.

How releases travel today (the facts the design rests on, as of v0.1.79):

- **Discovery is one call.** The panel's self-update check and the node-bundle sync both ask GitHub for
  `releases/latest` (`internal/panel/update/github_bundle.go` `fetchLatestRelease`) and refuse anything that is a draft,
  a prerelease or a tag with a pre-release suffix (`validReleaseTag` in `panel_update.go`: `semver.Prerelease(tag) == ""`).
  The "What's new" notes use `releases?per_page=30` with the same filter (`changelog.go`). A release marked
  *prerelease* on GitHub, or tagged `v0.1.80-dev.1`, is therefore invisible to every panel released so far.
- **Order is build time, not version text.** A panel installs a panel release only if the signed manifest's `built` is
  newer than its own binary's; the node-bundle sync never replaces the local bundle with an older one; an agent refuses a
  manifest whose `built` is older than its own (`ErrDowngrade`). Nodes never talk to GitHub: they get bytes from their
  panel, which keeps one trusted bundle in `<data-dir>/dist`.
- **The signed manifests have no room for new fields.** `manifest.json`, `panel-manifest.json` and `edge-manifest.json`
  are parsed with `DisallowUnknownFields` after a `schema == 1` check, so any new key or schema bump breaks every
  installed verifier. The `version` field is free text within `^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`: `v0.1.80-dev.1` is
  already a valid manifest version, and `mistgate release build` / `release sign` accept it.
- **The gates that refuse pre-release tags** are the release workflow's tag regex (`.github/workflows/release.yml`), the
  installer's `--version` regex (`scripts/install.sh`), `validReleaseTag` (discovery and notes) and the edge CLI's
  `releaseVersionRe` (`cmd/mistgate/edge.go`, which also keeps an unstamped `0.1.0-dev` build from guessing a release).
- **Settings** live in the key-value `setting` table; the Updates page talks to `UpdateService`
  (`proto/mistgate/admin/v1/update.proto`), whose mutations are owner-only with a step-up.

## Decision

### Three channels, and the tag says which

| Channel | Tag | GitHub release | Who it is for |
|---|---|---|---|
| `stable` | `vX.Y.Z` | normal, marked *latest* | everyone; the default |
| `testing` | `vX.Y.Z-testing.N` | *prerelease*, not *latest* | owners who want the next release early and can report problems |
| `dev` | `vX.Y.Z-dev.N` | *prerelease*, not *latest* | the maintainer's own panels and nodes: whatever is being built now |

- `X.Y.Z` is the stable version the work is heading for; `N` counts from 1 within that version and channel. Semver
  orders them as wanted: `v0.1.80-dev.3 < v0.1.80-testing.1 < v0.1.80`.
- The channel is a pure function of the tag (`release.ChannelOf`): no suffix is stable, `-testing.N` is testing,
  `-dev.N` is dev, anything else (`-rc.1`, `-beta`, a build suffix) is not a release and is refused everywhere.
- **No signed field changes.** The version string inside the signed manifests already carries the suffix, so the
  channel is signed with it, and old verifiers keep working. Nothing new is added to the manifests or their schema.
- Channel names are the same words in code, API, the installer flag and the docs. The admin shows them as
  *Stable*, *Testing*, *Development* (ru: *Стабильный*, *Тестовый*, *Разработка*).

### A panel follows one channel, and sees the channels above it

- The owner picks the panel's channel on the Updates page; it is stored in `setting` as `update_channel`
  (`stable` when unset, so every existing panel stays where it is).
- A panel on `stable` sees stable releases; on `testing`, testing and stable; on `dev`, all three. Among the published,
  non-draft releases it may see, it takes the highest semver. The existing build-time checks still decide whether it is
  newer than what runs: the channel only widens what is *offered*.
- Discovery moves from `releases/latest` to the release list (`releases?per_page=30`, with the ETag the notes already
  use), filtered by the channel. A stable panel's answer is the same release `releases/latest` gives today.
- The node bundle follows the panel's channel: the bundle sync takes the newest release the channel allows, with its own
  "never older than the local bundle" rule unchanged. Rollouts to nodes stay the owner's action, as today.
- "What's new" lists the releases the channel sees, each with its channel shown.

### Switching channels never downgrades

- To an earlier channel (`stable` → `testing` → `dev`): the next check offers the newest release of the wider set.
- To a later one (`dev` → `testing` → `stable`): nothing is installed backwards. The panel and its nodes stay on what they
  run until the chosen channel publishes something newer, and the Updates page says so ("v0.1.80-dev.3 stays until
  Stable has a newer release"). A deliberate downgrade remains a manual reinstall with `--version`, as now.
- Turning to `testing` or `dev` asks for a confirmation that says what the channel is; the change is owner-only, needs a
  step-up like the other update mutations, and is written to the audit log (`update.channel`).

### Installer and edge

- `scripts/install.sh --channel stable|testing|dev` (default `stable`) installs the newest release of that channel and the
  ones above it, found through the GitHub release list; `--version` also accepts `vX.Y.Z-testing.N` and `vX.Y.Z-dev.N`.
  The new panel starts on the channel it was installed from (the installer writes it on first start).
- `mistgate edge deploy` keeps deploying the edge release that matches its own binary; `releaseVersionRe` accepts the
  channel suffixes, still not an unstamped `0.1.0-dev`.

### The pipeline

- The release workflow accepts exactly `^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-(dev|testing)\.(0|[1-9]\d*))?$`.
  A testing or dev tag creates the draft with `--prerelease --latest=false`; a stable tag as today.
- **Where tags may point.** `stable` and `testing` tags must point at a commit on `main` or on a `release/*` branch (the
  workflow checks `git merge-base --is-ancestor`); `dev` tags may point anywhere, a feature branch included.
- **Promotion is of a commit, not of `main`.** The commit that was `v0.1.80-testing.2` becomes `v0.1.80` by tagging the
  same commit, even if `main` has moved on with unfinished work. Each tag is built, signed and published on its own; the
  binaries differ only in the version stamped in.
- **A fix for stable while `main` carries unfinished work**: branch `release/0.1` (or `release/0.1.80`) from the stable
  tag, cherry-pick the fix, tag `v0.1.81` there, and merge or cherry-pick the fix back to `main`.
- Signing and publishing are the same steps for every channel (docs/en/operations/releases.md); `--expires` may be shorter
  for dev (`30d`).
- **Release notes**: stable and testing get the full trilingual notes (en, ru, zh, split by `---`, as v0.1.77); a dev
  release gets at least a short trilingual list of what changed. The status page (`docs/*/roadmap/status.md`) names only
  stable releases.

### The admin

- **Updates page, "This panel" card**: a *Channel* control (segmented: Stable · Testing · Development) with one line saying
  what the chosen channel means; the running version and the offered one carry a small channel tag when they are not
  stable (`v0.1.80-testing.2 · Testing`).
- When the panel runs a newer pre-release than its channel offers (after switching to a later channel), the card says it
  stays on it until the channel catches up.
- Node rows show the agent's version as now; a non-stable one gets the same small tag.
- `GetUpdates` returns the channel, the running version's channel and the offered release's; `SetUpdateChannel` changes
  it; MCP read views show the channel (no MCP write tool).

## Consequences

- Panels released before this change see only stable releases, whatever is published: the channels are safe to start
  using as soon as the pipeline accepts the tags.
- A panel on `dev` gets everything the maintainer publishes there; its nodes get a dev agent only when the owner rolls
  it out. Agents need no change: they accept any correctly signed manifest with a newer build time.
- The version string becomes the single source of a release's channel; there is no channel metadata to keep in sync.
- More tags and releases on GitHub; testing and dev ones are prereleases, so the repository's *latest* and the default
  installer stay stable.
- A panel that moves to an earlier channel and back can sit on a pre-release for a while; that is the price of never
  downgrading automatically.

## Implementation plan

1. **Version and channel helpers** (`internal/release`): `ChannelOf(tag) (Channel, bool)`, the channel order, and one
   `ValidChannelTag` used by discovery, notes, the edge CLI and tests.
2. **Discovery** (`internal/panel/update`): the release list instead of `releases/latest` for the panel check and the
   bundle sync, filtered by the channel; notes filtered the same way; status carries the channels.
3. **Setting and API**: `update_channel` in `setting`; `UpdateService.SetUpdateChannel` (owner, step-up, audit);
   `GetUpdates` fields; MCP read view; migration not needed (key-value table).
4. **Installer and edge**: `--channel`, the wider `--version` regex, the first-start channel; `releaseVersionRe`.
5. **Workflow**: tag regex, ancestry check, `--prerelease --latest=false` for testing and dev.
6. **Admin UI**: the channel control, tags on versions, the "stays until" line, strings in en, ru, zh, fa and tr.
7. **Docs** (en, ru, zh): `operations/updates.md` (channels for owners), `operations/releases.md` (how to cut each
   channel, promotion, hotfix branches), `getting-started/install-panel.md` (`--channel`), and this record.
8. **Trial**: cut `v0.1.80-dev.1`, check that a stable panel does not see it and a dev panel offers it, then promote.

Go work (1-5) goes to Codex per the project's model routing; UI and docs to Claude; the main session reviews and merges.

## How to release on each channel (runbook)

```sh
# dev: any commit, usually main
git tag -a v0.1.80-dev.1 -m v0.1.80-dev.1 <commit> && git push origin v0.1.80-dev.1
# testing: a commit on main (or release/*) that is ready to be tried by others
git tag -a v0.1.80-testing.1 -m v0.1.80-testing.1 <commit> && git push origin v0.1.80-testing.1
# stable: the same commit that was tested
git tag -a v0.1.80 -m v0.1.80 <the commit of v0.1.80-testing.N> && git push origin v0.1.80
```

Then, for every tag: wait for the release workflow's draft, rebuild from the tag and sign (`release sign --expires 90d`,
`30d` for dev), upload the seven files, write the notes, publish (docs/en/operations/releases.md). For stable only: the
status page commit (current release, "New in the current release") before the tag.
