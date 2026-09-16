# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Builder** is a Go CLI tool for iOS development without a Mac. It has two main capabilities:
1. **Remote builds**: Build iOS apps via GitHub Actions from any platform
2. **Dev tools**: Hot reload on real iOS devices using MobAI (Flutter and React Native)

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
```

### Module Layout

```
cmd/builder/         # CLI entrypoint (Cobra)
internal/
  auth/              # GitHub OAuth device flow + keyring storage
  github/            # GitHub REST API (workflow dispatch, artifacts)
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
- **Build Profiles**: `profiles.<name>` in `builder.json` overrides `ios.configuration`, `ios.scheme`,
  `ios.signing` and `provider`, and adds `env` and the reserved `distribution`. `ios build` and
  `ios share` take `--profile`; without it `defaultProfile` applies, and without that the top-level
  settings are used unchanged. `config.ResolveProfile` does the merge, `Coordinator.settings` layers
  `--unsigned`/`--provider` on top, and `Progress.Settings` prints the result before dispatch.
  `Profile.Signing` is a `*bool` so a profile's `false` can override a top-level `true`; the jq in
  `Resolve parameters` needs an explicit `!= null` test for the same reason, since `//` treats
  `false` as missing. The runner receives env as one JSON object: the `profile` dispatch input
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
  `runner.sh`; the export step is meant to consume it under those names.
  `env` is build-time configuration, not secrets: it sits in `builder.json` and in the run's inputs
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

## Configuration

`builder.json`:
```json
{
  "project": "MyApp",
  "platform": "ios",
  "github": { "owner": "username", "repo": "my-ios-app" },
  "ios": { "path": "ios", "scheme": "" },
  "defaultProfile": "development",
  "profiles": {
    "development": { "configuration": "Debug", "signing": false },
    "preview":     { "configuration": "Release", "signing": true, "env": { "API_URL": "https://staging.example.com" } },
    "production":  { "configuration": "Release", "signing": true, "scheme": "MyApp", "provider": "codemagic", "distribution": "app-store" }
  }
}
```

`profiles` and `defaultProfile` are optional. A profile's fields are `configuration`, `scheme`,
`signing`, `provider`, `env` (string map) and `distribution` (`development`, `ad-hoc`, `app-store`,
`enterprise`; reserved for the export step, passed through but not applied yet). `runner` and
`submit` are planned for the same struct (`config.Profile`) but not read.

## Workflow Features

The embedded workflow template (`internal/workflow/templates/ios-build.yml`):
- Triggered via `workflow_dispatch` with `build_id`, `snapshot_ref`, `ios_path`, `scheme`,
  `use_signing`, `configuration`, `flutter_version`, `jdk_version` and `profile` (nine of the ten
  inputs GitHub allows; the last slot is meant for item 5's `build_number`, so add nothing else
  without combining)
- Dispatch runs the workflow from the **default branch**, so edits to the workflow file itself
  only take effect once pushed there — unlike app sources, which come from the snapshot ref
- Checks out `snapshot_ref` over the default-branch checkout when set
- Also triggered by pushing a tag `ios-build/<build-id>` (`ios-share/<build-id>` for the share
  workflow) for environments without GitHub API access. Push events run the workflow file from
  the tagged commit, `inputs` are empty, so a `Resolve parameters` step reads `ios_path`, `scheme`,
  `use_signing`, `configuration`, `flutter_version` and `jdk_version` from `builder.json` in the
  tagged tree, applying the profile named by `defaultProfile` (a tag cannot pick one per run);
  every later step reads `steps.params.outputs.*`, never `inputs.*`. The same step exports the
  profile's `env` to `$GITHUB_ENV` and outputs `profile` and `distribution`. The job deletes
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
