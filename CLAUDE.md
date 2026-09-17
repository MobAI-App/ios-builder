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
./builder ios build --profile production  # Build with a builder.json profile
./builder dev flutter       # Flutter hot reload with MobAI
./builder dev rn            # React Native hot reload with MobAI
./builder dev kmp           # Kotlin Multiplatform install + launch (no hot reload)
./builder dev flutter --skip-install --bundle-id <id>  # Use already installed app
./builder dev rn --skip-install --bundle-id <id>       # Use already installed app
./builder auth apple        # Save an App Store Connect API key
./builder signing setup --devices-from-mobai   # development: certificate + devices + profile via the ASC API, secrets to GitHub, profile in builder.json
./builder signing setup --distribution store --yes --json  # Distribution certificate + App Store profile, no prompts
./builder ios build --profile store            # Signs with the STORE set; provisions it first when the secrets are missing
./builder ios upload --wait # Upload dist/*.ipa to App Store Connect, wait for processing
./builder ios submit --testflight --group <name> --notes <text>  # TestFlight
./builder ios submit --app-store --release after-approval        # App Review
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

builder signing setup ───► Bundle ID: --bundle-id → ios.bundleId → dist/*.ipa → prompt
                                │
                                ▼
                          App Store Connect API (signing.Auto)
                            ├─ bundleIds?filter[identifier] → POST bundleIds
                            ├─ certificates?filter[certificateType] → reuse if the
                            │     key matches, else CSR → POST certificates → .p12
                            ├─ devices?filter[platform]=IOS → POST devices (dev/ad-hoc)
                            └─ profiles?filter[name] → reuse / DELETE + POST profiles
                                │
                                ▼
                          Writes key/.p12/.mobileprovision named by distribution, uploads the
                          IOS_*_<SET> trio to GitHub (failure printed, non-zero exit at the end),
                          prints names + values, writes profiles.<name>.distribution

builder ios build --profile X ─► ResolveProfile: distribution → set, signing, configuration
                                │
                                ▼
                          GitHub: ListSecretNames; all three IOS_*_<SET> present → dispatch
                            else ASC key → signing.Auto (no prompts) + upload → dispatch
                            else fail naming `auth apple` / `signing setup --certificate`

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
  asc/               # App Store Connect API client (JWT, JSON:API, builds, uploads, TestFlight, review,
                     #   bundle IDs, certificates, devices, profiles)
  distribute/        # Upload / TestFlight / App Store flows on top of asc
  ipa/               # Info.plist reading from .ipa archives
  build/             # Build coordination (snapshot + trigger + poll + download)
  signing/           # CSR generation, .p12 assembly, and Auto (portal-free provisioning on top of asc)
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
- **Run Failures**: a run that completes without success is a `github.RunFailedError`: conclusion,
  first failed job/step, and that job's `failure` annotations (`/check-runs/{job_id}/annotations`; a
  job ID is its check run ID). The details are best-effort, so the conclusion is always reported
- **Build Profiles**: `profiles.<name>` overrides `ios.configuration`/`ios.scheme`/`provider` and adds
  `env` and `distribution` (`config.ResolveProfile`: `--profile`, else `defaultProfile`, else top level
  unchanged). A profile signs iff it has a `distribution`; `ios.signing` is only the no-profile path
- **Profile Transport**: the `profile` dispatch input is one JSON object (`{"name","env","distribution"}`)
  to stay under the ten-input limit and is sent only when a profile is selected, since an older
  workflow rejects unknown inputs (`triggerError`). `runner.sh` reads `BUILD_ENV` and `DISTRIBUTION`
- **Profile Env**: entries are base64 per key/value on the runner and the `$GITHUB_ENV` heredoc uses a
  random delimiter; names must match `^[A-Za-z_][A-Za-z0-9_]*$` and not hit `reservedEnv`/
  `reservedEnvPrefixes` (`internal/config/profile.go`), which must track what the runners read
- **Signing Sets**: one trio per distribution, `IOS_{CERTIFICATE,CERTIFICATE_PASSWORD,PROVISIONING_PROFILE}_<SET>`
  (DEVELOPMENT, AD_HOC, STORE, ENTERPRISE); the unsuffixed names serve only the legacy no-profile path.
  The table lives in `config.SigningSet` and the shell `signing_set` (both templates) and must agree
- **Signing Step**: `select_signing_set` (indirect expansion; a suffixed set needs all three, only the
  legacy password may be empty) then `check_signing_set` compares `detect_export_method` with the
  distribution before any keychain exists. Shared functions are verbatim in both templates; tests diff them
- **Signing Setup**: writes only its distribution's set and `profiles.<name>.distribution` (other fields
  and an equal spelling kept), never `ios.signing` or `defaultProfile`. Upload always targets the `github`
  repo in builder.json; a failure is printed, values still shown, exit non-zero (`github_upload` in `--json`)
- **On-Demand Provisioning**: `ensureSigningSecrets` (GitHub only, before any push) lists secret names
  (403/404 = missing scope/admin, never "no secrets") and provisions a missing set via `signing.Auto` with
  no prompts, key from `signing.dir` then `.`; a certificate 409 with no local key names the dirs searched
- **Flutter Detection**: Auto-detects Flutter projects, runs `flutter pub get`, uses `Runner` scheme
- **Expo Detection**: an `expo` dependency in `package.json` (the CLI parses the dependency maps;
  the runners grep `'"expo"'`) with no `.xcodeproj`/`.xcworkspace` anywhere and no `pubspec.yaml` is
  a managed project. `detectIOSPath` still returns `ios`, because that is where `expo prebuild` puts
  the project on the runner; nothing is generated locally, and managed projects gitignore `ios/`
  so the snapshot carries none. All three runners (`ios-build.yml`, `ios-share.yml`, `runner.sh`)
  prebuild with `CI=1` after the node install and before the Pods step, skip it when the iOS path
  already holds an Xcode project (ejected), and fail with a named error when the app config has no
  `ios.bundleIdentifier` — without one `expo prebuild` prompts and the job would hang. The steps
  that walk the iOS path before that (XcodeGen, base-configuration check) skip a missing directory.
- **JS Package Manager**: React Native and Expo dependencies install with the manager the
  project declares — `packageManager` in `package.json` first, then the lockfile
  (`pnpm-lock.yaml`, `yarn.lock`, `bun.lock`/`bun.lockb`, `package-lock.json`), else npm.
  pnpm and yarn come from `corepack` (installed with npm where Node 25+ or a provider image
  lacks it), bun from its installer, and `expo prebuild` and the other `npx` calls run
  through the same manager (`pnpm exec`/`yarn`/`bunx`/`npx`). Running
  `npm install` on a pnpm or yarn workspace fails with `EUNSUPPORTEDPROTOCOL Unsupported URL
  Type "workspace:"`, so the guess is not free. The Node version is `.nvmrc`/`.node-version`
  (as `node-version-file`), else `engines.node` with the range prefix stripped, else 22;
  `with:` cannot be conditional per key, so a `Resolve JS toolchain` step computes both and
  setup-node ignores the empty one. The shell for all of this is one block between
  `# >>> js toolchain` and `# <<< js toolchain`, repeated verbatim in `ios-build.yml` (twice),
  `ios-share.yml` (twice) and `runner.sh`; `TestJSToolchainBlockIdentical` fails on drift.
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
- **ASC Client** (`internal/asc`): runs locally, never on the runner; ES256 JWT (15 min, cached)
  from the `.p8`, generic JSON:API plumbing (`getOne`/`getAll`/`post`/`patch`, `getAll` follows
  `links.next`). 429 retries on any method, 5xx only off POST; every wait goes through `Client.sleep`.
- **ASC Credentials**: one JSON secret (`apple-asc-key`) in the keyring/file store, via the shared
  `readSecret`/`writeSecret`/`deleteSecret` helpers. `ASC_ISSUER_ID`, `ASC_KEY_ID` +
  `ASC_PRIVATE_KEY`|`ASC_KEY_PATH` win; a partial environment is an error. Only `auth apple` prompts,
  and it verifies with `GET /v1/certificates?limit=1`, which needs the access signing needs.
- **Build Upload**: `buildUploads` → `buildUploadFiles` (returns `uploadOperations`) → PUT each byte
  range with its `requestHeaders`, no bearer token → PATCH `uploaded=true` → poll the upload `state`,
  then `builds` until VALID. The IPA must be App Store signed with an ever-higher `CFBundleVersion`.
- **Export Compliance**: a build sits in "Missing Compliance" until `usesNonExemptEncryption` is
  answered; `upload --wait` PATCHes it from the plist or `--no-encryption`. The build must exist
  first, so without `--wait` it falls to `submit`, which refuses unanswered builds for TestFlight.
- **Submit Order**: TestFlight is compliance → notes → `betaAppReviewSubmissions` (only for a new
  external group) → add groups. App Store reuses an open `reviewSubmission`, skips an item the
  version is already in, and rewrites ASC 409/422 with a "complete the metadata" hint.
- **Automatic Signing** (`signing.Auto`): idempotent, never revokes. A certificate is reused only when
  its key is local (`--key`, `ios-signing-<distribution>.key`, legacy `ios-signing.key`; PKCS#8 written,
  PKCS#1 still read). Profile `Builder <d> <bundle id>` is recreated on INVALID/expired/`--force`/changes
- **ASC Signing Gotchas**: `filter[identifier]` on bundleIds is a prefix match (exact checked client-side);
  membership comes from `/relationships/{certificates,devices}` (`include=` caps arrays); store profiles
  send no `devices` relationship; enterprise is refused; `signingtest` must not import `signing`
- **Export Method Follows The Profile**: `detect_export_method` (both templates) reads the type from the
  set's profile into ExportOptions.plist `method` (legacy names: older Xcodes reject the 15.3+ ones);
  `check_signing_set` maps `app-store` → `store` when comparing with the distribution
- **Signing Identity Follows The Profile Type**: `signing_identity` picks `CODE_SIGN_IDENTITY` from
  `security find-identity` right after import: `Apple Development`/`iPhone Developer` for development,
  `Apple Distribution`/`iPhone Distribution` otherwise; without it Xcode keeps the project's default
- **Signing Settings Live In The pbxproj**: `apply_signing_to_app_target` (both templates, right before
  each signed archive, after `pod install`/`expo prebuild`/`flutter build ios`) writes the four manual
  settings into app targets only via `plutil`; on the command line every Pods target would inherit them
- **App Target Selection**: one app target is signed whatever its bundle id (the export reports a
  mismatch); with several, those whose `PRODUCT_BUNDLE_IDENTIFIER` `PROFILE_BUNDLE_ID` covers (`*`
  wildcards), else `::error::` naming the ids found; conditional `NAME[sdk=…]` variants are dropped
- **Extension Targets**: `ios.extensions` lists their bundle ids (`init`/`signing setup` append what
  `xcodeproj.ExtensionBundleIDs` finds; managed Expo lists by hand). `signing.Auto` makes an App ID and
  `Builder <d> <id>` profile per entry, uploaded as `IOS_EXTENSION_PROFILES_<SET>` (JSON of id → base64,
  always written, `{}` for none; required by `missingSigningSecrets` only when the list is non-empty).
  Manual mode: one `--extension-profile` each, matched by the profile's app id. The runner's
  `install_extension_profiles` installs them and hands `EXTENSION_PROFILES` (id → name) to
  `apply_signing_to_app_target`, which signs each extension-type target (same list as
  `xcodeproj.ExtensionProductTypes`, tested) with the longest covering entry, or fails naming the
  targets and ids to add; `write_export_options` adds them to `provisioningProfiles`. Secrets are
  unreadable, so a build re-provisions when the project has extensions builder.json did not list
- **Extension Points**: a future `ios release` composes `distribute.Upload` and
  `distribute.SubmitTestFlight`, reading `asc.Client.ListBuilds` for the latest build number; the
  `pkg/` wrappers do not expose `asc` yet.

## Configuration

`builder.json`:
```json
{
  "project": "MyApp",
  "platform": "ios",
  "github": { "owner": "username", "repo": "my-ios-app" },
  "ios": { "path": "ios", "scheme": "", "bundleId": "com.example.app" },
  "defaultProfile": "development",
  "profiles": {
    "development": { "distribution": "development" },
    "preview":     { "distribution": "internal", "env": { "API_URL": "https://staging.example.com" } },
    "production":  { "distribution": "store", "scheme": "MyApp", "provider": "codemagic" }
  }
}
```

`ios.bundleId` is optional: `init` fills it when the project has exactly one app target, `signing
setup` saves what it resolved. `signing.dir` is the last automatic `signing setup`'s `--out-dir` as
given (`~` kept, omitted for `.`); on-demand provisioning looks there for the key first.

A profile's fields are `distribution` (`development`, `ad-hoc`/`internal`, `store`, `enterprise`; the
only signing field, omitted = unsigned), `configuration` (else Debug for development, Release
otherwise), `scheme`, `provider`, `env`. `runner`/`submit` are planned on `config.Profile`, not read.

## Workflow Features

The embedded workflow template (`internal/workflow/templates/ios-build.yml`):
- Triggered via `workflow_dispatch` with `build_id`, `snapshot_ref`, `ios_path`, `scheme`, `use_signing`,
  `configuration`, `flutter_version`, `jdk_version` and `profile`: nine of the ten inputs GitHub
  allows, and the last slot is reserved for `build_number`, so combine before adding one
- Dispatch runs the workflow from the **default branch**, so edits to the workflow file itself
  only take effect once pushed there — unlike app sources, which come from the snapshot ref
- Checks out `snapshot_ref` over the default-branch checkout when set
- Also triggered by pushing a tag `ios-build/<build-id>` (`ios-share/<build-id>` for the share
  workflow) for environments without GitHub API access. Push events run the workflow file from
  the tagged commit, `inputs` are empty, so a `Resolve parameters` step reads `ios_path`, `scheme`,
  `use_signing`, `configuration`, `flutter_version` and `jdk_version` from `builder.json` in the
  tagged tree, applying `defaultProfile` (a tag cannot pick a profile per run); every later step
  reads `steps.params.outputs.*`, never `inputs.*`. The same step exports the profile's `env` to
  `$GITHUB_ENV` and outputs `profile`, `distribution` and `signing_set`. The job deletes
  the tag when it ends (`permissions: contents: write`). Any other workflow in the repo with an
  unfiltered `on: push` also fires on these tags.
- Runs on `macos-latest`
- Detects Flutter projects (checks for `pubspec.yaml`)
- Restores and saves DerivedData for fast incremental builds
- Auto-detects workspace/project and scheme
- Flutter: uses `Runner` scheme, runs `flutter pub get`
- Installs CocoaPods if Podfile exists
- Builds unsigned IPA with `CODE_SIGNING_ALLOWED=NO`
- **Export Method**: `detect_export_method` maps `ProvisionsAllDevices` → `enterprise`, `ProvisionedDevices`
  + `get-task-allow` → `development`, without → `ad-hoc`, neither → `app-store`; non-development exports
  set `manageAppVersionAndBuildNumber = false`, and a distribution profile with `Debug` fails before the build
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
