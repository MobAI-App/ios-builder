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
./builder ios release --profile store --group <name> --notes <text>  # Build with the next build number, upload, wait, TestFlight
./builder ios release --profile store --app-store --release after-approval  # Same, then App Review
./builder ios build --profile store --submit                          # Short for: ios release (no groups)
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
                          Writes key/.p12/.mobileprovision (named by distribution), uploads
                          the three IOS_*_<SET> secrets of the distribution's set to GitHub
                          (a failed upload is printed, not fatal; non-zero exit at the end),
                          always prints their names and values (Codemagic/Bitrise paste them),
                          writes profiles.<name>.distribution

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

builder ios release ─────► Preflight: API key; --profile/defaultProfile has distribution
                            store and configuration Release; STORE set provisioned on
                            demand (ensureSigningSecrets, GitHub only)
                                │
                                ▼
                          Bundle ID (--bundle-id, ios.bundleId, newest dist/*.ipa)
                            └─ ListBuilds (all versions) → max CFBundleVersion + 1
                                │
                                ▼
                          ios build with build_number input (N or X.Y.Z+N)
                            └─ runner: flutter --build-number / CURRENT_PROJECT_VERSION /
                               plist rewrite when CFBundleVersion is hardcoded
                                │
                                ▼
                          Reads dist IPA, refuses it unless CFBundleVersion == N
                                │
                                ▼
                          distribute.Upload (wait) → SubmitTestFlight | SubmitAppStore
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
  release/           # ios release / build --submit: next build number, build, verify, upload, submit
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
- **Build Profiles**: `profiles.<name>` in `builder.json` overrides `ios.configuration`, `ios.scheme`
  and `provider`, and adds `env` and `distribution`. `ios build` and `ios share` take `--profile`;
  without it `defaultProfile` applies, and without that the top-level settings are used unchanged.
  `config.ResolveProfile` does the merge, `Coordinator.settings` layers `--unsigned`/`--provider` on
  top, and `Progress.Settings` prints the result before dispatch. `distribution` is the only
  signing field of a profile (EAS-style): `development`, `ad-hoc` (alias `internal`, canonical
  `ad-hoc`; `config.ParseDistribution`), `store`, `enterprise`. A profile signs iff it has one;
  `ios.signing` applies only when no profile is selected (legacy, unsuffixed secrets). Its
  configuration is the one it sets, else Debug for `development` and Release for the rest, else
  `ios.configuration`. The jq in `Resolve parameters` derives the same for tag builds (`$p != ""`
  guards, since `.profiles[""]` is null). The runner receives env as one JSON object: the `profile` dispatch input
  (`{"name","env","distribution"}`, one input to stay under the ten-input limit) on GitHub, and
  `BUILD_ENV` plus `DISTRIBUTION` variables for `runner.sh`. Each entry is base64-encoded per
  key and value on the runner (jq drops NUL bytes, and a key with a space must not split), the
  `$GITHUB_ENV` heredoc uses a random delimiter so no value line can end it early, names are
  checked against `^[A-Za-z_][A-Za-z0-9_]*$`, and `ResolveProfile` rejects the names the runners
  own (`reservedEnv` and `reservedEnvPrefixes` in `internal/config/profile.go`: the runner
  parameters, the signing secrets, `PATH`/`HOME`/`DEVELOPER_DIR`, and the `GITHUB_`, `RUNNER_`,
  `CM_`, `BITRISE_`, `BUILDER_` namespaces; keep that list in step with what `runner.sh` and the
  workflows read). `profile` is only sent when a profile is selected (`--profile` or
  `defaultProfile`), because a workflow file from before profiles rejects a dispatch with an input
  it does not declare; `triggerError` turns that 422 into a "run `builder init`" message. On
  GitHub the profile's env lands in `$GITHUB_ENV`, and step-level `env:` (the signing secrets, the
  build parameters) takes precedence over it. `distribution` reaches the runner as the
  `steps.params.outputs.distribution` output on GitHub and the `DISTRIBUTION` variable for
  `runner.sh`, where it selects the signing set (below).
  `env` is build-time configuration, not secrets: it sits in `builder.json` and in the run's inputs
- **Signing Sets**: one trio of secrets per distribution, `IOS_CERTIFICATE_<SET>`,
  `IOS_CERTIFICATE_PASSWORD_<SET>`, `IOS_PROVISIONING_PROFILE_<SET>` with SET the canonical
  distribution upper-cased, `-` → `_`: DEVELOPMENT, AD_HOC (also for `internal`), STORE,
  ENTERPRISE. The unsuffixed names serve only the legacy no-profile path (`ios.signing`); a
  profile never falls back to them. The distribution → set table exists twice and must agree:
  `config.SigningSet` (Go; `config.SigningSecretNames` builds the names, `""` → legacy) and the
  shell function `signing_set` in `ios-build.yml`'s `Resolve parameters` (emits the `signing_set`
  output; canonicalizes `internal`) and `runner.sh` (`install_signing` derives it from
  `DISTRIBUTION`). The signing step receives every set's secrets as env (GitHub hands a missing
  secret over as empty; Codemagic/Bitrise users define the suffixed variables);
  `select_signing_set` picks the set by bash indirect expansion, or with an empty set the
  unsuffixed names. A suffixed set needs all three secrets, password included (Builder never
  writes one without): a partial set fails naming the missing names; only the unsuffixed password
  may be empty, as before. `check_signing_set` compares `detect_export_method`'s result with the
  requested distribution canonically (`app-store` → `store`, `internal` → `ad-hoc`; the legacy set
  is never checked) after the profile is decoded and before any keychain exists or `security
  import` runs. `select_signing_set`/`check_signing_set`/`signing_set` are verbatim in both
  templates, each with its own `fail` (`::error::` vs stderr); `TestSigningSetSelection` compares
  the bodies and runs them with stub secrets. `signing setup` writes only the set of its
  distribution (automatic: `--distribution`, else the `--name` profile's, else development;
  manual: `signing.ProfileType` reads the plist out of the CMS blob and a disagreeing
  `--distribution` is an error) and never touches other sets or the legacy names, then writes
  `profiles.<--name or distribution>.distribution` (`writeSigningProfile`: other fields kept, an
  equal distribution keeps the user's spelling, a different one is replaced and the old value
  printed; `defaultProfile` is never set) and never `ios.signing`. Both modes always upload to the
  `github` repository in builder.json (no `--provider`, no `provider` field) and always print the
  three names with where their values come from, for Codemagic, Bitrise or a repository the token
  cannot write to; a failed upload (or a GitHub client that cannot be built) is an `Error:` line on
  stderr, everything else is still written and printed, and only the exit code is non-zero
  (`github_upload` in `--json`: `ok` or the error). Files are `ios-signing-<distribution>.key/.p12`, so two coexist in
  one `--out-dir`; the key lookup is `--key`, then the distribution's file, then the legacy
  `ios-signing.key`. `Progress.Settings` prints `signed (set X)` / `signed (unsuffixed IOS_*
  secrets)`. Enterprise is a valid set and profile type but `Auto` refuses it (no ASC endpoint
  for in-house profiles). The suffixed secret names and `SIGNING_SET*` are reserved env names.
- **On-Demand Provisioning** (`ensureSigningSecrets` in `cmd/builder/signing_auto.go`, called by
  `runBuild` without `--unsigned`; it returns early unless the provider that will run the job —
  `--provider`, else the profile's, else the top level, as `Coordinator.settings` resolves it — is
  GitHub): when the selected profile has a distribution, `github.Client.ListSecretNames`
  (`GET /repos/{o}/{r}/actions/secrets`, paginated; 403/404 are reported as a token without the
  `repo` scope or admin access, never as "no secrets") is checked for the three names; all
  present → dispatch. Otherwise, with an ASC key (`getASCClient` passed as a
  factory so tests inject the `signingtest` portal), `signing.Auto` plus `uploadSigningSet` (shared
  with `signing setup`, but fatal here) runs with no prompts: bundle ID from `ios.bundleId` or `dist/*.ipa`,
  key from `.`, generated password (printed once), no devices given (Auto covers the enabled ones
  and fails naming `signing setup --distribution development --devices-from-mobai` when there are
  none). Without an ASC key the error names `builder auth apple` and `signing setup --certificate
  ... --profile ...`, before anything is pushed. Codemagic/Bitrise skip the check (no secrets API).
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
  an error, not a fallback. Only `auth apple` prompts; `upload`/`submit` never do. `auth apple`
  verifies the key with `GET /v1/certificates?limit=1` (as MobAI does): `apps?limit=1` answers 200
  for a key of any role, `certificates` needs the Certificates, Identifiers & Profiles access that
  signing needs and every role that can upload builds has.
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
- **Automatic Signing** (`signing.Auto`, behind `signing setup` without `--certificate`/
  `--profile` and behind on-demand provisioning): idempotent and never revokes. `signing.Type`'s
  values are the canonical distributions (`signing.ParseType` wraps `config.ParseDistribution`).
  A certificate is reused only when its private key is local (`--key`, or the
  `ios-signing-<distribution>.key` / legacy `ios-signing.key` a previous run left in
  `--out-dir`), since a .p12 needs the key; otherwise a new one is issued and Apple's quota error
  (2 Development / 3 Distribution) gets a hint. Keys are written as PKCS#8 (`PRIVATE KEY`, as
  MobAI's signer writes them); the PKCS#1 `RSA PRIVATE KEY` files of earlier runs are still read. Dev/ad-hoc profiles cover every ENABLED iOS
  device on the account, not just the ones passed; App Store profiles send no `devices`
  relationship at all (an empty one is rejected). Profile membership is read from
  `/v1/profiles/{id}/relationships/{certificates,devices}` (paginated), not `include=`, which
  caps linkage arrays. The profile `Builder <distribution> <bundle id>` is recreated when
  INVALID, expired, `--force`, or when the certificate/device set differs; same-named duplicates
  are deleted with it. `filter[identifier]` on bundleIds is a prefix match, so the exact
  identifier is checked client-side. The in-memory portal for tests is
  `internal/signing/signingtest` (must not import `signing`: the signing package's own tests use it).
- **Export Method Follows The Profile**: the `method` in ExportOptions.plist must match the
  uploaded profile's type (`development`, `ad-hoc`, `app-store`, `enterprise`), or xcodebuild
  refuses the export. `detect_export_method` in `ios-build.yml` and `runner.sh` reads it from
  the profile of the selected signing set, and `check_signing_set` confirms it is the type the
  build profile's `distribution` asked for (`app-store` is the `store` distribution).
- **Release Flow** (`internal/release`): `ios release` and `ios build --submit` share `release.Run`,
  which takes a `Builder` interface (`*build.Coordinator`) and an `*asc.Client`, so tests use a
  fake builder that writes an IPA plus an httptest ASC. `Preflight(cfg, profile)` resolves the
  profile (`--profile`, else `defaultProfile`) and refuses to dispatch unless its distribution is
  `store` (TestFlight accepts nothing else either) and its effective configuration is `Release`
  (derived for `store`; an explicit `Debug` is refused with the hint); `ios.signing`/
  `ios.configuration` play no part. Without a store profile the error names `builder signing
  setup --distribution store` (which writes the `store` profile) and `--profile store`. The
  command checks the API key, then `Preflight`, then `ensureSigningSecrets` for a GitHub build
  (missing `STORE` secrets are provisioned like `ios build --profile`), all before the snapshot
  push; `Run` calls `Preflight` again so the package is safe on its own. `--unsigned` with
  `--submit` is refused. The cobra layer stays thin and reuses `finish`/`newOutput` from
  `upload.go`. `--timeout` bounds the build and then the ASC wait separately. `Coordinator.Build`
  takes `*BuildOptions`.
- **Automatic Build Numbers**: ASC rejects an upload whose `CFBundleVersion` is not above every
  processed build, so `release` lists the app's builds across all marketing versions
  (`ListBuilds`, no limit, pages through `links.next`) and increments the last component of the
  largest (`1` when none; dotted numbers compare component-wise). `--build-number` skips the
  query. The number travels as the single `build_number` dispatch input (`BUILDER_BUILD_NUMBER`
  for Codemagic/Bitrise — Codemagic predefines `BUILD_NUMBER` as its own counter, so `runner.sh`
  maps the prefixed name onto it), encoded `N` or `X.Y.Z+N` when `--version` is given — one input because
  `workflow_dispatch` caps inputs at 10. `apply_build_number` in both templates must stay
  byte-identical; `TestApplyBuildNumber` extracts it from each and diffs them. It validates the
  shape (the value reaches `eval` and `plutil`), passes `--build-number`/`--build-name` to
  `flutter build`, appends `CURRENT_PROJECT_VERSION`/`MARKETING_VERSION` to every `xcodebuild`,
  and when the app target's `INFOPLIST_FILE` (from `-showBuildSettings`) holds a literal
  `CFBundleVersion` rather than `$(CURRENT_PROJECT_VERSION)`/`$(FLUTTER_BUILD_NUMBER)`, rewrites
  it with `plutil -replace` and logs which path it took. Back home, `release.Run` reads the IPA
  and fails if `CFBundleVersion` differs from the request, so a missed case never reaches ASC as
  an opaque duplicate. Plain `ios build` sends no input and behaves as before; the runner also
  ignores an empty `BUILD_NUMBER`.

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

`ios.bundleId` is optional: `init` fills it from `PRODUCT_BUNDLE_IDENTIFIER` when the Xcode
project has exactly one app target (test targets and `$(…)` values are skipped), and
`signing setup` saves whatever it resolved. `ios release` uses it to find the App Store Connect
app before the first IPA exists (otherwise the newest `dist/*.ipa`).

`profiles` and `defaultProfile` are optional. A profile's fields are `distribution`
(`development`, `ad-hoc`/`internal`, `store`, `enterprise`; the only signing field: selects the
signing set and the profile type the runner expects, and with it the export method; omitted is
unsigned), `configuration` (derived from the distribution when omitted: Debug for development,
Release otherwise), `scheme`, `provider` and `env` (string map). `ios.signing` is the legacy
no-profile path with the unsuffixed secrets. `runner` and `submit` are planned for the same struct
(`config.Profile`) but not read.

## Workflow Features

The embedded workflow template (`internal/workflow/templates/ios-build.yml`):
- Triggered via `workflow_dispatch` with `build_id`, `snapshot_ref`, `ios_path`, `scheme`,
  `use_signing`, `configuration`, `flutter_version`, `jdk_version`, `profile` and `build_number`
  (all ten inputs GitHub allows, so add nothing else without combining; `TestGitHubInputsMapping`
  counts them)
- Dispatch runs the workflow from the **default branch**, so edits to the workflow file itself
  only take effect once pushed there — unlike app sources, which come from the snapshot ref.
  A dispatch naming an input the file lacks fails with 422 "Unexpected inputs"; `triggerError`
  turns that into a hint to rerun `builder init` when `profile` or `build_number` was sent
- Checks out `snapshot_ref` over the default-branch checkout when set
- Also triggered by pushing a tag `ios-build/<build-id>` (`ios-share/<build-id>` for the share
  workflow) for environments without GitHub API access. Push events run the workflow file from
  the tagged commit, `inputs` are empty, so a `Resolve parameters` step reads `ios_path`, `scheme`,
  `use_signing`, `configuration`, `flutter_version` and `jdk_version` from `builder.json` in the
  tagged tree, applying the profile named by `defaultProfile` (a tag cannot pick one per run;
  `build_number` is always empty there); every later step reads `steps.params.outputs.*`, never
  `inputs.*`. The same step exports the profile's `env` to `$GITHUB_ENV` and outputs `profile`,
  `distribution` and `signing_set`. The job deletes
  the tag when it ends (`permissions: contents: write`). Any other workflow in the repo with an
  unfiltered `on: push` also fires on these tags.
- Runs on `macos-latest`
- Detects Flutter projects (checks for `pubspec.yaml`)
- Restores and saves DerivedData for fast incremental builds
- Auto-detects workspace/project and scheme
- Flutter: uses `Runner` scheme, runs `flutter pub get`
- Installs CocoaPods if Podfile exists
- Builds unsigned IPA with `CODE_SIGNING_ALLOWED=NO`
- **Export Method**: `detect_export_method` reads the profile — `ProvisionsAllDevices` →
  `enterprise`, `ProvisionedDevices` with `get-task-allow` → `development`, without → `ad-hoc`,
  neither → `app-store` — and that method goes into `ExportOptions.plist` (legacy names, since
  older Xcodes reject the 15.3+ ones). Non-development exports add
  `manageAppVersionAndBuildNumber = false`, and a distribution profile with configuration `Debug`
  fails in the signing step, before the build. The function is duplicated verbatim in
  `ios-build.yml` and `runner.sh`; a test compares the two bodies and runs one against
  synthetic profile plists
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
