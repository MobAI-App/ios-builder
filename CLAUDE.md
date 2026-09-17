# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Builder** is a Go CLI tool for iOS development without a Mac. It has three main capabilities:
1. **Remote builds**: Build iOS apps via GitHub Actions from any platform
2. **Dev tools**: Hot reload on real iOS devices using MobAI (Flutter and React Native)
3. **Distribution**: Upload builds to App Store Connect and submit them to TestFlight or App Review

## Build Commands

```bash
# Build
go build -o builder ./cmd/builder

# Test
go test ./...

# Install
go install ./cmd/builder

# Run
./builder auth github       # Authenticate with GitHub (OAuth device flow)
./builder init              # Set up workflow in current repo
./builder ios build         # Trigger build and download IPA to ./dist/
./builder dev flutter       # Flutter hot reload with MobAI
./builder dev rn            # React Native hot reload with MobAI
./builder dev kmp           # Kotlin Multiplatform install + launch (no hot reload)
./builder dev flutter --skip-install --bundle-id <id>  # Use already installed app
./builder dev rn --skip-install --bundle-id <id>       # Use already installed app
./builder auth apple        # Save an App Store Connect API key
./builder ios upload --wait # Upload dist/*.ipa to App Store Connect, wait for processing
./builder ios submit --testflight --group <name> --notes <text>  # TestFlight (creates the group if missing)
./builder ios submit --app-store --release after-approval        # App Review
./builder asc apps|builds|groups|testers|users                   # App Store Connect listings (--json)
./builder asc groups create <name> [--external]                  # also: groups delete, groups add-build
./builder asc testers add <email>... --group <name>              # also: testers remove, users invite
./builder asc testers invite <email>...                          # send/resend the TestFlight email
./builder asc builds expire --build-number N --yes               # groups delete needs --yes too
```

## Architecture

```
builder auth github ─────► GitHub OAuth (device flow)
                                │
                                ▼
                          Token stored in OS keychain

builder init ────────────► Detects git remote (origin)
                                │
                                ▼
                          Creates .github/workflows/ios-build.yml
                          Creates builder.json
                          Optionally commits/pushes and runs build

builder ios build ───────► Snapshots working tree (git commit-tree)
                                │
                                ▼
                          Pushes to refs/ios-builder/jobs/<build-id>
                                │
                                ▼
                          Triggers workflow_dispatch with snapshot_ref
                                │
                                ▼
                          GitHub Actions (macos-14)
                            ├─ Checks out the snapshot ref
                            ├─ Detects Flutter/native
                            ├─ Caches DerivedData
                            ├─ Builds unsigned IPA
                            └─ Uploads artifact
                                │
                                ▼
                          Downloads IPA to ./dist/
                                │
                                ▼
                          Deletes the snapshot ref

builder dev flutter ─────► Connects to MobAI
                                │
                                ▼
                          Installs IPA on device (with optional re-sign)
                                │
                                ▼
                          Launches app with debugger
                                │
                                ▼
                          Captures VM Service URL
                                │
                                ▼
                          Runs flutter attach (hot reload)

builder dev rn ──────────► Starts Metro bundler (if not running)
                                │
                                ▼
                          Connects to MobAI
                                │
                                ▼
                          Installs IPA on device (with optional re-sign)
                                │
                                ▼
                          Launches app with Metro URL env vars

builder dev kmp ─────────► Connects to MobAI
                                │
                                ▼
                          Installs IPA on device (with optional re-sign)
                                │
                                ▼
                          Launches app and streams output (no hot reload)

builder ios upload ──────► Reads bundle ID / version / build number from dist/*.ipa
                                │
                                ▼
                          App Store Connect API (ES256 JWT from the .p8 key)
                            ├─ apps?filter[bundleId]
                            ├─ POST buildUploads → POST buildUploadFiles
                            ├─ PUT chunks to presigned URLs
                            ├─ PATCH buildUploadFiles uploaded=true
                            └─ --wait: poll buildUploads state, then builds → VALID
                                │
                                ▼
                          PATCH builds usesNonExemptEncryption (plist / --no-encryption)

builder ios submit ──────► Picks the newest VALID build (or --build-number)
                            ├─ --testflight: betaBuildLocalizations (notes),
                            │     betaAppReviewSubmissions (external groups),
                            │     builds/{id}/relationships/betaGroups
                            └─ --app-store: appStoreVersions (find/create, attach
                                  build, releaseType), reviewSubmissions +
                                  reviewSubmissionItems, PATCH submitted=true
```

### Module Layout

```
cmd/builder/         # CLI entrypoint (Cobra)
internal/
  auth/              # GitHub OAuth device flow + keyring storage (also CI tokens, ASC API key)
  github/            # GitHub REST API (workflow dispatch, artifacts)
  asc/               # App Store Connect API client (JWT, JSON:API, apps, builds, uploads, TestFlight,
                     #   beta groups, beta testers, team users/invitations, review)
  distribute/        # Upload / TestFlight / App Store / tester flows on top of asc
  ipa/               # Info.plist reading from .ipa archives
  build/             # Build coordination (snapshot + trigger + poll + download)
  signing/           # CSR generation and .p12 assembly (signing without a Mac)
  snapshot/          # Working-tree snapshot as a throwaway commit on a remote ref
  workflow/          # Workflow template (embedded)
  config/            # builder.json management
  dev/               # Development session (Flutter/React Native hot reload)
  mobai/             # MobAI API client (device control, app install/launch)
```

## Key Patterns

- **OAuth Device Flow**: No gh CLI dependency, uses GitHub's device authorization
- **Keyring Storage**: Token stored via `go-keyring` (macOS Keychain, Windows Credential Manager, Linux SecretService)
- **Interactive Init**: Prompts to commit/push and run first build
- **Remote Selection**: `--remote` flag to use non-origin remotes
- **Working-Tree Snapshot**: `ios build` builds what is on disk, including uncommitted and
  untracked files. `git write-tree`/`commit-tree` against a temporary index produce a commit
  parented on HEAD; the branch, index and working tree are never modified. The commit is pushed
  to `refs/ios-builder/jobs/<build-id>`, which is outside `refs/heads` so it creates no branch
  and fires no `push` events, and is deleted when the build finishes.
- **Snapshot Exclusions**: `.gitignore` applies, so ignored files (`.env`, `GoogleService-Info.plist`,
  local `*.xcconfig`) are absent from the build. Submodules are recorded as gitlinks, so a
  submodule commit that only exists locally fails checkout on the runner.
- **Run Correlation**: `run-name` carries the build ID so concurrent builds cannot adopt each
  other's runs
- **Flutter Detection**: Auto-detects Flutter projects, runs `flutter pub get`, uses `Runner` scheme
- **DerivedData Caching**: `restore` keys on `github.run_id` and only the prefix in `restore-keys`
  ever hits, so every run must pair with a `cache/save` step or later builds stay cold. `ios-share`
  saves before it shares the simulator, since that step blocks until the session ends.
- **Scheme Selection**: `xcodebuild -list -json` plus the scheme named after the workspace/project;
  taking the first scheme picks a package or pod scheme in package-heavy repos
- **Product Selection**: the built `.app` comes from `-showBuildSettings -json` (the target whose
  `PRODUCT_TYPE` is an application), because the product name often differs from the scheme name
  and a repo can have several app targets
- **Missing xcconfig**: a gitignored `*.xcconfig` with a committed `*.xcconfig.template` (either
  suffix order) is copied into place before the build; anything still referenced by the project and
  absent fails the job by name instead of as xcodebuild's opaque "Unable to open base configuration
  reference file". Pods/Flutter-generated configs are skipped — they appear later in the job.
- **MobAI Integration**: HTTP/WebSocket API for device control, app install, debug launch
- **MobAI Device Claims**: every command that drives a device (`dev *` sessions, `mobai install`,
  `mobai run-debug`, `mobai forward`) calls `mobai.Client.Claim` first, and the client sends the
  lease as `X-Lease-Token` on every request and the debug WebSocket. MobAI demands it from network
  callers (WSL) and from local ones when its "Require device claim" setting is on. The claim ID is
  stable per install (`ios-builder/mobai-client-id` in the user config dir), so Flutter's separate
  `builder mobai` processes share one lease. Long-running commands run `KeepLease`, since MobAI
  renews only on requests. Builder never releases: MobAI refuses network claims on a running
  bridge nobody holds, so a release would lock the next WSL run out
- **MobAI Access Key**: MobAI with an API token set rejects every non-loopback call but health with
  401. The client reads `MOBAI_ACCESS_KEY` from the environment, else `.env` in the working
  directory, and sends it as `X-API-Key` on requests and the debug WebSocket. Flutter runs the
  custom device commands from the project directory, so they find the same `.env`
- **Flutter Custom Devices**: Auto-configures `~/.config/flutter/custom_devices.json` for `mobai-ios` device
- **Debug URL Capture**: WebSocket stream captures VM Service URL from app launch
- **React Native Metro**: Auto-starts Metro bundler, passes Metro URL to app via environment variables
- **FrameworkHandler Interface**: Common session with pluggable handlers for Flutter/React Native/KMP
- **KMP Detection**: The multiplatform Gradle plugin is matched by regex in root and module build
  files. `cmd/builder/root.go` (`kmpPluginRe`) and both workflow templates must agree: a project the
  CLI calls KMP but the runner does not gets no JDK, and vice versa.
- **KMP Has No Hot Reload**: shared Kotlin compiles to a native framework at build time, so
  `dev kmp` only installs, launches and streams output; code changes need `ios build`
- **ASC Client** (`internal/asc`): runs locally, never on the runner. Auth is an ES256 JWT
  (15 min, cached, refreshed a minute early) signed with the `.p8` key. JSON:API plumbing is
  generic (`Document`/`Resource[A]`, `getOne`/`getAll`/`post`/`patch`); typed helpers exist only
  for what the commands use, so the signing resources (bundle IDs, certificates, profiles,
  devices) add files in the same package without restructuring. `getAll` follows `links.next`;
  429 retries on every method, 5xx only on idempotent ones (a failed POST may have created the
  resource). All waits go through `Client.sleep`, which tests replace, so retry and poll tests
  run instantly; status polls (`poller`) grow 1.5× per round up to 4× the base interval.
  `*asc.Error` carries the ASC `errors[]` and renders on one line.
- **ASC Credentials**: one JSON secret (`apple-asc-key`) in the keyring/file store, via the
  shared `readSecret`/`writeSecret`/`deleteSecret` helpers the CI tokens use. `ASC_ISSUER_ID`,
  `ASC_KEY_ID` + `ASC_PRIVATE_KEY`|`ASC_KEY_PATH` take precedence; a partially set environment is
  an error, not a fallback. Only `auth apple` prompts; `upload`/`submit` never do.
- **Build Upload**: `buildUploads` → `buildUploadFiles` (returns `uploadOperations`) → PUT each
  byte range with its `requestHeaders`, no bearer token → PATCH `uploaded=true` → poll the upload
  `state` (COMPLETE/FAILED with `errors[]`) → poll `builds` filtered by app, marketing version and
  build number until VALID. No checksum is sent (asc-cli found ASC rejects some encodings). The IPA
  must be App Store signed and each upload needs a higher `CFBundleVersion`.
- **Export Compliance**: a build sits in "Missing Compliance" until `usesNonExemptEncryption` is
  answered. `upload --wait` PATCHes it to false when Info.plist says `ITSAppUsesNonExemptEncryption`
  false or `--no-encryption` is given; the build must exist first, so without `--wait` it is left
  for `submit --no-encryption`. `submit --testflight` refuses to add an unanswered build to groups.
- **Submit Order**: TestFlight is compliance → notes → `betaAppReviewSubmissions` (only when a
  chosen group is external and none exists) → add groups. App Store reuses an open
  `reviewSubmission` (READY_FOR_REVIEW/UNRESOLVED_ISSUES), skips the item when the version is
  already in it, and rewrites ASC 409/422 with a "complete the metadata" hint.
- **Group Auto-Create**: `SubmitTestFlight` creates any `--group` name the app lacks (internal,
  or external with `External`/`--external`); existing groups keep their type. `GroupRef.Created`
  marks them in the JSON result. `asc groups add-build` reuses `SubmitTestFlight`, so it inherits
  this and the beta-review step.
- **Automatic Distribution Groups**: an internal group with `hasAccessToAllBuilds: true` gets every
  build by itself and `POST builds/{id}/relationships/betaGroups` answers 422 for it. The add-build
  path skips such groups with a note (`GroupRef.AutoBuilds`, exit 0); `asc groups create` sends
  `hasAccessToAllBuilds: true` for internal groups unless `--no-auto-builds`.
- **Internal Testers**: internal groups take team members only. `distribute.AddTester` routes by
  group type: external → `asc.Client.AddBetaTester` (POST `betaTesters` with the group, 409 → find
  by email → POST `betaGroups/{id}/relationships/betaTesters`); internal → `GET users?filter
  [username]`, then the member's tester record joins the group, or a stranger gets
  `POST userInvitations` (`CUSTOMER_SUPPORT`, `visibleApps` = this app) and the status
  `team_invite_sent`/`team_invite_pending`; the build reaches them only after they accept and the
  command reruns. Apple's email/username filters are substring matches, so `Find*` compare exactly;
  `filter[email]` is sent lowercased because ASC stores addresses that way.
- **NOT_INVITED Testers**: a team member put into an internal group (in the UI or by the API)
  keeps `state: NOT_INVITED` and gets no email until `POST betaTesterInvitations` (relationships
  `app` + `betaTester`; the response has no attributes, so the state is read back with
  `GET betaTesters/{id}`). `AddTester` re-reads the state after a group add and invites when it is
  still NOT_INVITED; `asc testers invite` (`distribute.InviteTester`) does it on demand for
  NOT_INVITED/INVITED records and leaves ACCEPTED/INSTALLED alone.
- **Group Name Matching**: `asc.MatchBetaGroup` is the only name lookup (command layer and
  `findOrCreateGroup`): case-insensitive, nil when absent, and an error listing the candidates when
  several groups fold to the same name, so nothing is created, deleted or linked on a guess.
- **Destructive asc Commands**: `groups delete`, `testers remove` without `--group` and
  `builds expire` resolve everything first, print a "Will ..." line naming exactly what goes, and
  then need `--yes`; `testers remove` looks every address up before the first deletion.
- **asc Command Layer**: `cmd/builder/asc.go` is thin cobra over `asc` and `distribute`. The app is
  resolved once by `resolveApp` (`--bundle-id` → `--ipa` → `ios.bundleId` in builder.json → newest
  `dist/*.ipa`), shared with `ios submit`, which takes the marketing version from the IPA only
  when one was read; `runTestFlight` is the submit-and-print step `ios submit --testflight` and
  `asc groups add-build` share. `getASCClient` is a package var so command tests point it at an
  httptest server; `run` in the tests resets every cobra flag first, since values persist on the
  shared command tree. Listings are `[]row` structs with snake_case JSON tags and `text/tabwriter`
  columns; builds use `include=preReleaseVersion,betaGroups` and the `included` block
  (`collect`/`includedAttr`).
- **Extension Points**: a future `ios release` (upload + TestFlight, automatic build numbers)
  composes `distribute.Upload` and `distribute.SubmitTestFlight` and reads `asc.Client.ListBuilds`
  for the latest build number; the `pkg/` wrappers do not expose `asc` yet.

## Configuration

`builder.json`:
```json
{
  "project": "MyApp",
  "platform": "ios",
  "github": { "owner": "username", "repo": "my-ios-app" },
  "ios": { "path": "ios", "scheme": "", "bundleId": "com.example.myapp" }
}
```

`ios.bundleId` is optional; the `asc` commands fall back to the newest IPA in `./dist/`.

## Workflow Features

The embedded workflow template (`internal/workflow/templates/ios-build.yml`):
- Triggered via `workflow_dispatch` with `build_id`, `snapshot_ref`, `ios_path`, `scheme`
- Dispatch runs the workflow from the **default branch**, so edits to the workflow file itself
  only take effect once pushed there — unlike app sources, which come from the snapshot ref
- Checks out `snapshot_ref` over the default-branch checkout when set
- Also triggered by pushing a tag `ios-build/<build-id>` (`ios-share/<build-id>` for the share
  workflow) for environments without GitHub API access. Push events run the workflow file from
  the tagged commit, `inputs` are empty, so a `Resolve parameters` step reads `ios_path`, `scheme`,
  `use_signing`, `configuration`, `flutter_version` and `jdk_version` from `builder.json` in the
  tagged tree; every later step reads `steps.params.outputs.*`, never `inputs.*`. The job deletes
  the tag when it ends (`permissions: contents: write`). Any other workflow in the repo with an
  unfiltered `on: push` also fires on these tags.
- Runs on `macos-latest`
- Detects Flutter projects (checks for `pubspec.yaml`)
- Restores and saves DerivedData for fast incremental builds
- Auto-detects workspace/project and scheme
- Flutter: uses `Runner` scheme, runs `flutter pub get`
- Installs CocoaPods if Podfile exists
- Builds unsigned IPA with `CODE_SIGNING_ALLOWED=NO`
- Uploads IPA as GitHub artifact with 7-day retention

## Flutter Dev Requirements

- MobAI running with physical iOS device connected (no simulators)
- App must be closed on device before launching
- Re-signing requires iCloud account (recommend creating a new one)
- After re-sign, bundle ID has team suffix (e.g., `com.example.app.TEAMID`)
- Only rebuild (`ios build`) for native code changes; Dart changes use hot reload

## React Native Dev Requirements

- MobAI running with physical iOS device connected (no simulators)
- Device and computer must be on the same WiFi network (for Metro connection)
- Node.js and React Native CLI installed
- Metro bundler started automatically or manually (`npx react-native start`)
- App must be closed on device before launching
- Re-signing requires iCloud account (recommend creating a new one)
- After re-sign, bundle ID has team suffix (e.g., `com.example.app.TEAMID`)
- Only rebuild (`ios build`) for native code changes; JS changes use hot reload
