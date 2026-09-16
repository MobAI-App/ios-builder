# Builder

Build and develop iOS apps from Windows, Linux, or any platform.

Builder is a CLI tool for iOS development without a Mac. It uses GitHub Actions (default), Codemagic, or Bitrise for remote builds and [MobAI](https://mobai.run) for on-device development.

![Builder Demo](assets/ios-builder-demo.gif)

## Features

- **Build from anywhere**: Build iOS apps via GitHub Actions, Codemagic, or Bitrise
- **Independent provider logins**: Stay signed in to all three and choose where each build runs
- **Try it on a simulator**: Use your build on an iOS simulator from Windows or Linux
- **Flutter & React Native dev tools**: Hot reload on real iOS devices from Windows/Linux
- **Simple setup**: One command to add the workflow to your repo
- **Code signing**: Optional signing with your certificate and provisioning profile
- **TestFlight and App Store**: Upload builds and submit them for review through the App Store Connect API, from any platform
- **Device integration**: Install and run apps via MobAI

## How It Works

```
Your Repository                  GitHub Actions (macOS)
 └─ .github/workflows/            └─ ios-build.yml
     └─ ios-build.yml                 ├─ Check out the snapshot
                                      ├─ Build with Xcode
builder ios build ───────────────────► Upload IPA artifact
     │  pushes a snapshot of
     │  your working tree
     └─ Downloads IPA ◄─────────────── artifact: ipa
```

`builder ios build` builds what is on disk, not your last commit: uncommitted
and untracked files are included, so you can try a change without committing
it. The snapshot is a throwaway commit pushed to a hidden ref that is deleted
when the build finishes; no branch is created and nothing is committed on your
behalf. `.gitignore` still applies, so ignored files such as `.env` or
`GoogleService-Info.plist` are absent from the build.

## Quick Start

### 1. Authenticate with GitHub

```bash
builder auth github
```

### 2. Initialize (in your project directory)

```bash
cd your-ios-project
builder init
```

This detects your GitHub repo, creates the workflow files, and offers to commit, push, and trigger your first build - all interactively.

### 3. Build

```bash
builder ios build
```

The CLI triggers the workflow and downloads the IPA to `./dist/`.

### 4. Try it on a simulator (optional)

```bash
builder ios share
```

Builds the working tree for the iOS simulator and makes that simulator usable
from the [MobAI](https://mobai.run) app, so you can tap through a build without
a Mac. It shows up under CI Devices, stays available while you are using it, and
closes when you release it there or leave it unused (30 minutes by default, use
`--duration` to change). A coding agent connected to MobAI (Claude Code, Codex,
Cursor) can drive the simulator the same way.

Free with any MobAI account, on [MobAI 3.0 or later](https://mobai.run). Needs
a `MOBAI_API_KEY` repository secret: create the key in the MobAI app under
Account → API Keys, then:

```bash
gh secret set MOBAI_API_KEY
```

### Triggering from git only

Where the GitHub API is not reachable, both workflows can also be started by
pushing a tag. Commit the tree you want built, then:

```bash
git tag ios-build/my-build && git push origin ios-build/my-build   # IPA build
git tag ios-share/my-build && git push origin ios-share/my-build   # simulator
```

The run is named after the tag. Build settings come from `builder.json` in the
tagged commit (`ios.path`, `ios.scheme`, `ios.signing`, `ios.configuration`,
`flutter.version`, `kmp.jdkVersion`), the simulator stays available for the
default 30 minutes, and the tag is deleted when the run ends. The IPA is
attached to the run as an artifact. A tag carries no flags, so a tag build
cannot pick a [profile](#build-profiles) per run; it applies the profile named
by `defaultProfile`, if there is one.

## Additional macOS Providers

GitHub Actions remains the default, so existing commands continue to work. Add
Codemagic and Bitrise without logging out of GitHub. First follow the
[app creation and repository connection guide](docs/provider-setup.md) to create
each provider app, authorize GitHub access, and find its app ID:

```bash
builder auth codemagic
builder auth bitrise
builder auth status
builder init --provider codemagic --app-id YOUR_APP_ID --branch main
builder init --provider bitrise --app-id YOUR_APP_SLUG --branch main
builder ios build --provider codemagic
builder ios build --provider bitrise
```

`init` writes `codemagic.yaml` or `bitrise.yml` at the repo root plus the shared
runner script `.builder/ci/runner.sh`. Commit them to the configured branch
and connect the same repository to each provider before building. See
[provider setup, signing, simulator sessions, and free allowances](docs/providers.md).

## Supported Frameworks

| Framework | iOS Path | Auto-detected |
|-----------|----------|---------------|
| Native iOS/Swift | `.` (root) | Yes |
| React Native | `ios/` | Yes |
| Expo (ejected) | `ios/` | Yes |
| Flutter | `ios/` | Yes |
| Kotlin Multiplatform | `iosApp/` | Yes |
| Cordova/Ionic | `platforms/ios/` | Yes |

## Installation

### Windows

Download `builder-windows-amd64.exe` from [Releases](https://github.com/MobAI-App/ios-builder/releases), rename it to `builder.exe`, and add it to PATH.

### Homebrew (macOS/Linux)

```bash
brew install mobai-app/tap/ios-builder
```

The formula is named `ios-builder`; the command it installs is `builder`.

### macOS/Linux/WSL

```bash
curl -sSL https://raw.githubusercontent.com/MobAI-App/ios-builder/main/install.sh | bash
```

### From Source

```bash
git clone https://github.com/MobAI-App/ios-builder.git
cd ios-builder
go build -o builder ./cmd/builder
```

## Commands

```bash
# Setup
builder auth github           # Authenticate with GitHub
builder auth codemagic        # Authenticate with Codemagic (also: bitrise)
builder auth apple            # Save an App Store Connect API key
builder auth status           # Show which providers you are signed in to
builder auth logout [name]    # Remove stored credentials (github, codemagic, bitrise, apple)
builder init                  # Set up workflows in current repo
builder update                # Update builder to the latest release

# Building (builds the working tree, including uncommitted changes)
builder ios build             # Trigger build and download IPA to ./dist/
builder ios build --unsigned  # Build without code signing (if signing is configured)
builder ios build --provider codemagic  # Build on another provider (also: bitrise)
builder ios build --profile production  # Build with a profile from builder.json

# Simulator (free, needs a MOBAI_API_KEY secret)
builder ios share             # Try the build on a simulator in the MobAI app
builder ios share --duration 1h  # Keep it available longer while unused

# Development (requires MobAI)
builder dev flutter           # Flutter hot reload with file watching
builder dev flutter --no-watch  # Disable automatic file watching
builder dev flutter --no-attach # Print flutter attach command instead of running it
builder dev rn                # React Native hot reload (short for: dev react-native)
builder dev kmp               # Kotlin Multiplatform install + launch (alias: kotlin)
builder dev kmp --logs        # Also stream the app's output
builder dev flutter --skip-install --bundle-id <id>  # Use already installed app
builder dev rn --metro-port 8082  # Use custom Metro port

# MobAI (used by the dev commands; handy for troubleshooting)
builder mobai ping            # Check MobAI connectivity
builder mobai install <ipa>   # Install an IPA on the device
builder mobai run-debug <bundle-id>  # Launch an app with the debugger attached
builder mobai forward <device-port> <host-port>  # Forward a device port

# Code signing (automatic mode needs builder auth apple)
builder signing setup --devices-from-mobai       # development: certificate, devices, profile, GitHub secrets, no portal
builder signing setup --distribution store       # Apple Distribution certificate + App Store profile
builder signing setup --certificate ios-signing.p12 --profile MyApp.mobileprovision  # Upload your own files
builder ios build --profile store                # Signs with the set; provisions it first when missing
builder signing csr           # Manual path: create a private key + certificate signing request
builder signing p12           # Manual path: assemble a .p12 from the key and Apple's certificate

# TestFlight and App Store (needs builder auth apple)
builder ios upload --wait     # Upload ./dist/*.ipa to App Store Connect and wait for processing
builder ios submit --testflight --group "Beta Testers" --notes "What to test"
builder ios submit --app-store --release after-approval  # Submit the version for App Review
```

Every `upload`/`submit` command takes `--json` for machine-readable output and
never prompts, so agents and CI jobs can drive them.

## Configuration

`builder.json`:

```json
{
  "project": "MyApp",
  "platform": "ios",
  "github": {
    "owner": "username",
    "repo": "my-ios-app"
  },
  "ios": {
    "path": "ios",
    "scheme": "",
    "bundleId": "com.example.app",
    "configuration": "Debug"
  },
  "profiles": {
    "development": { "distribution": "development" },
    "store":       { "distribution": "store" }
  },
  "mobai": {
    "url": "http://localhost:8686",
    "device_id": ""
  },
  "flutter": {
    "watch": {
      "dirs": ["lib"],
      "patterns": [".dart"],
      "ignore": [".g.dart", ".freezed.dart"],
      "debounce": 100
    }
  }
}
```

### iOS Build Configuration

| Field | Description | Default |
|-------|-------------|---------|
| `ios.path` | Path to the Xcode project relative to the repo root | detected by `init` |
| `ios.scheme` | Xcode scheme to build | auto-detected |
| `ios.bundleId` | App bundle identifier, used by `signing setup` | detected by `init` when the project has one app target; else saved by `signing setup` |
| `ios.configuration` | Xcode build configuration. **Builds are `Debug` unless you set `Release`**; Debug is faster and is what the dev commands expect | `Debug` |
| `ios.signing` | Legacy: sign builds that select no profile, with the unsuffixed `IOS_CERTIFICATE`, `IOS_CERTIFICATE_PASSWORD` and `IOS_PROVISIONING_PROFILE` secrets. Profiles ignore it; use `distribution` there | `false` |

### Build Profiles

Profiles are named sets of build settings, in the spirit of `eas.json`, selected
with `--profile` on `ios build` and `ios share`:

```json
{
  "ios": { "path": "ios", "bundleId": "com.example.app" },
  "defaultProfile": "development",
  "profiles": {
    "development": { "distribution": "development" },
    "preview":     { "distribution": "internal",
                     "env": { "API_URL": "https://staging.example.com" } },
    "production":  { "distribution": "store", "scheme": "MyApp", "provider": "codemagic" }
  }
}
```

```bash
builder ios build --profile preview
builder ios share --profile preview
```

| Field | Description |
|-------|-------------|
| `distribution` | The only signing setting: `development`, `ad-hoc` (or `internal`, the same thing), `store` or `enterprise`. The build signs with that distribution's [signing set](#code-signing) and its provisioning profile must be of that type; the IPA is exported with the matching method. Omitted means an unsigned build |
| `configuration` | Overrides the derived configuration: `Debug` for `development`, `Release` for every other distribution, `ios.configuration` for unsigned profiles |
| `scheme` | Overrides `ios.scheme` |
| `provider` | Overrides the top-level `provider` (`github`, `codemagic`, `bitrise`) |
| `env` | String map exported as environment variables on the runner before dependencies are installed and the app is built, so `pod install`, `npm install`, `flutter pub get`, Gradle and xcodebuild all see them |

How a build's settings are resolved:

- Without `--profile`, the profile named by `defaultProfile` applies. With
  neither, the top-level `ios.*` and `provider` settings are used exactly as
  before, so existing projects are unaffected.
- A profile only overrides the fields it sets; everything else comes from the
  top level. An unknown profile name is an error that lists the available ones.
- `--unsigned` and `--provider` on the command line override the profile.
- The resolved settings (profile, configuration, scheme, signing set, provider,
  env names) are printed before anything is dispatched.
- `ios share` only takes the profile's scheme, provider and env: simulator
  builds are always Debug and unsigned.

**`env` values are build-time configuration, not secrets.** They are stored in
`builder.json`, sent to the CI provider as plain workflow inputs, and visible in
the run's inputs and logs. Keep tokens and passwords in the provider's secrets
instead (`gh secret set` on GitHub, or the [Codemagic / Bitrise secrets
guide](docs/provider-secrets.md)); the build reads those as environment
variables too. Names the runner owns are rejected: its own parameters
(`SCHEME`, `CONFIGURATION`, `USE_SIGNING`, `BUILD_ENV`, ...), the signing
secrets, `PATH`, `HOME`, `DEVELOPER_DIR`, and anything starting with `GITHUB_`,
`RUNNER_`, `CM_`, `BITRISE_` or `BUILDER_`.

Selecting a profile, with `--profile` or `defaultProfile`, needs the workflow
files from this version of Builder, which declare a `profile` input; an older
committed workflow rejects the dispatch. Run `builder init` again to refresh
`.github/workflows/ios-build.yml` and `ios-share.yml` (or `builder init
--provider ...` for `runner.sh`) in a project set up earlier, then commit and
push them to the default branch.

### MobAI Configuration

| Field | Description | Default |
|-------|-------------|---------|
| `mobai.url` | MobAI API URL | `http://localhost:8686` |
| `mobai.device_id` | Preferred device ID (uses first available if empty) | `""` |

**WSL users**: MobAI runs on Windows, and WSL has its own network by default. On
Windows 11, turn on
[mirrored networking](https://learn.microsoft.com/en-us/windows/wsl/networking#mirrored-mode-networking)
and builder reaches MobAI on the default `http://localhost:8686`. See
[Using Builder from WSL](docs/wsl.md) for the steps, and for the setup without
mirrored networking.

### Flutter File Watcher

| Field | Description | Default |
|-------|-------------|---------|
| `flutter.watch.dirs` | Directories to watch | `["lib"]` |
| `flutter.watch.patterns` | File patterns to match | `[".dart"]` |
| `flutter.watch.ignore` | Patterns to ignore | `[".g.dart", ".freezed.dart"]` |
| `flutter.watch.debounce` | Debounce delay in ms | `100` |

## Code Signing

By default, builds are unsigned. A signed build needs a certificate and a
provisioning profile — and despite what many guides claim, **you do not need a
Mac to create either one**, nor a tour of the Apple Developer portal. Signing
is configured per [build profile](#build-profiles) with one field,
`distribution`, and `builder signing setup` produces the material for it
through the App Store Connect API (or takes your own files).

You need a paid [Apple Developer Program](https://developer.apple.com/programs/)
membership — Apple only issues certificates to paid accounts. (Without one,
build unsigned and let [MobAI](https://mobai.run) re-sign on install with a free
Apple ID.)

### Profiles and signing sets

Each distribution has its own set of three GitHub secrets, so a development set
for your devices and a store set for TestFlight live side by side:

| `distribution` | Certificate, profile | Secrets |
|----------------|----------------------|---------|
| `development` | Apple Development, iOS App Development (devices required) | `IOS_CERTIFICATE_DEVELOPMENT`, `IOS_CERTIFICATE_PASSWORD_DEVELOPMENT`, `IOS_PROVISIONING_PROFILE_DEVELOPMENT` |
| `ad-hoc` or `internal` | Apple Distribution, Ad Hoc (devices required) | `IOS_CERTIFICATE_AD_HOC`, `IOS_CERTIFICATE_PASSWORD_AD_HOC`, `IOS_PROVISIONING_PROFILE_AD_HOC` |
| `store` | Apple Distribution, App Store | `IOS_CERTIFICATE_STORE`, `IOS_CERTIFICATE_PASSWORD_STORE`, `IOS_PROVISIONING_PROFILE_STORE` |
| `enterprise` | In-house (portal only) | `IOS_CERTIFICATE_ENTERPRISE`, `IOS_CERTIFICATE_PASSWORD_ENTERPRISE`, `IOS_PROVISIONING_PROFILE_ENTERPRISE` |

A build with `--profile <name>` signs with the set of that profile's
`distribution`; `configuration` follows it (`Debug` for `development`,
`Release` otherwise) unless the profile sets one. The runner checks that the
profile in the set is of the requested type and fails by name before compiling
anything, and a distribution profile refuses a `Debug` configuration. On
Codemagic and Bitrise the same names are variables you add in the dashboard,
see the [secrets guide](docs/provider-secrets.md).

### `builder signing setup`

```bash
builder auth apple                                  # once: save the App Store Connect API key
builder signing setup --devices-from-mobai          # development set for the devices MobAI sees
builder signing setup --distribution store          # store set for TestFlight / App Store
```

Without files, `setup` works through the App Store Connect API for the given
`--distribution` (default `development`; `--name <profile>` reads it from an
existing profile). The key needs the **Admin** role (or App Manager plus
*Access to Certificates, Identifiers & Profiles*): Developer-role keys cannot
create certificates. It then:

1. Registers the **App ID** if the bundle identifier is not on the account yet.
   The bundle ID comes from `--bundle-id`, `ios.bundleId` in `builder.json`
   (which `init` fills when the Xcode project has a single app target), or the
   newest IPA in `./dist/`; in a terminal it asks as a last resort.
2. Issues a **certificate** — Apple Development for `development`, Apple
   Distribution for `ad-hoc` and `store` — for a private key generated on your
   machine (`ios-signing-<distribution>.key`, or `--key` to reuse one from
   `signing csr`; a `ios-signing.key` from an earlier version is picked up
   too). A valid certificate on the account is reused only when its private
   key is here, because that is the only way to build the `.p12`; otherwise a
   new one is issued. Nothing is ever revoked: when Apple's limit (2
   Development, 3 Distribution) is hit, the error names it and points at the
   portal.
3. Registers **devices** from `--device <udid>` (repeatable) and
   `--devices-from-mobai` (name and UDID of every physical iOS device MobAI has
   connected; simulators and cloud farm devices are skipped). Development and
   ad-hoc profiles cover every enabled iOS device on the account, so with none
   given and none registered the command stops and says so. Store profiles
   take no devices. Apple allows 100 devices per membership year and never
   frees a slot; that error is passed through too.
4. Creates the **profile** `Builder <distribution> <bundle id>`. An existing
   one is reused while it is `ACTIVE`, unexpired and still lists exactly this
   certificate and these devices; otherwise it is deleted and recreated, and
   the summary says why (`invalid`, `expired`, `certificate changed`, `devices
   changed`, `forced`).
5. Writes `ios-signing-<distribution>.key` (when generated),
   `ios-signing-<distribution>.p12` and `Builder-<distribution>-<bundle
   id>.mobileprovision` to `--out-dir` (default `.`), uploads the three secrets
   of the set to GitHub, and writes the build profile in `builder.json`:
   `--name` (default: the distribution name) with `"distribution":
   "<distribution>"`. Other fields of an existing profile are kept; a
   different `distribution` in it is replaced, and the command says so.
   `defaultProfile` is not touched: point it at the profile for a plain
   `ios build` to use it, or pass `--profile`.
6. Prints the three secret names and where their values come from — the
   `.p12` base64-encoded, the password, the `.mobileprovision` base64-encoded
   — every time, so the same set can be pasted into Codemagic or Bitrise,
   following the [secrets guide](docs/provider-secrets.md). Builder cannot
   check those providers' secrets before a build, so `ios build` only reminds
   you of this command when the profile signs there.

The upload goes to the repository in `builder.json`, always. When it fails (no
GitHub login, or a token that cannot write secrets) the error is printed and
the command carries on: files, values and the build profile are written and
shown anyway, and it exits non-zero at the end so a script notices. `--json`
reports the same in `github_upload` (`ok` or the error).

The command shows its plan and asks once before creating anything; `--yes`
skips that (required without a terminal), and then the `.p12` password is
generated and printed once unless `--password` is given. `--json` prints the
result as JSON with progress on stderr. Keep the written files out of git.
Run it again whenever you like: it reports what it found and recreates only
what is missing, expired, invalid or changed — add a device, re-run, rebuild.
`--force` issues a fresh certificate and profile regardless.

With `--certificate` and `--profile`, `setup` takes your own files instead — a
`.p12` (from Keychain Access, or [assembled here](#manual-path-through-the-apple-developer-portal))
and a `.mobileprovision` — reads the distribution out of the profile
(development, ad-hoc, store or enterprise; this is the only way in for
enterprise), uploads that set, prints its names and values, and writes the
build profile the same way:

```bash
builder signing setup --certificate ios-signing.p12 --profile MyApp.mobileprovision
```

### Provisioning from `ios build`

`builder ios build --profile <name>` checks, before dispatching to GitHub,
that the repository holds all three secrets of the profile's set. When any is
missing and an App Store Connect key is saved, it runs the same provisioning as
`signing setup` without prompts, uploads the set and then builds. A development
or ad-hoc profile needs at least one registered device; with none, the build
stops and points at `builder signing setup --distribution development
--devices-from-mobai`. Without an Apple key the build stops before anything is
pushed and names both ways out: `builder auth apple`, or `builder signing setup
--certificate ... --profile ...`. `--unsigned` skips all of this, and
Codemagic/Bitrise builds skip the check (no secrets API): their runner
reports a missing set itself.

### Legacy: `ios.signing` without profiles

A project set up before build profiles has `ios.signing: true` and the
unsuffixed `IOS_CERTIFICATE`, `IOS_CERTIFICATE_PASSWORD` and
`IOS_PROVISIONING_PROFILE` secrets. Builds that select no profile still sign
with those, whatever the profile type, exactly as before; `setup` never
touches them. Profiles ignore `ios.signing` and read their own set.

### Manual path through the Apple Developer portal

The `.p12` certificate is normally created through Keychain Access, but Builder
does the same thing itself: it generates the private key and certificate
signing request, and assembles the `.p12` from the certificate Apple issues.

#### 1. Create a certificate signing request

```bash
builder signing csr
```

This asks for your name and email and writes two files to the current
directory: `ios-signing.key` (your private key) and `ios-signing.csr`. Keep
the key wherever suits you — just don't commit it (add it to `.gitignore`;
gitignored files are also excluded from build snapshots).

#### 2. Create the certificate

1. Go to [Certificates](https://developer.apple.com/account/resources/certificates/add) on the Apple Developer portal
2. Choose **Apple Development** (installs on registered devices) or **Apple Distribution** (App Store/Ad Hoc). TestFlight and App Store uploads need **Apple Distribution** together with an App Store profile in step 4
3. Upload `ios-signing.csr` and download the resulting `.cer` file

#### 3. Assemble the .p12

```bash
builder signing p12 --certificate development.cer --key ios-signing.key
```

This combines the key and certificate into `ios-signing.p12` (`--out` to name
it), protected by a password you choose — byte-for-byte the same kind of file
Keychain Access exports, and usable anywhere one is: `builder signing setup`,
Sideloadly, AltStore, or importing it on a Mac. Keep it, and don't commit it.

#### 4. Create a provisioning profile

On the portal:

1. **Identifiers** → register an App ID matching your app's bundle identifier
2. **Devices** → register your device's UDID (shown in [MobAI](https://mobai.run) when the device is connected; on Windows, iTunes shows it when you click the serial number on the device page)
3. **Profiles** → create an **iOS App Development** (or Ad Hoc, App Store) profile, select your App ID, certificate, and devices, then download the `.mobileprovision` file

The profile type decides the distribution the set is written to and the method
the IPA is exported with: development, ad-hoc, enterprise or store.

#### 5. Upload the signing secrets

```bash
builder signing setup --certificate ios-signing.p12 --profile MyApp.mobileprovision
```

You can also skip step 3 and hand `setup` the `.cer` together with the key —
`builder signing setup --certificate development.cer --key ios-signing.key
--profile MyApp.mobileprovision` — and it assembles the `.p12` on the way,
saving it as `ios-signing-<distribution>.p12`. Then `builder ios build
--profile <name>`; `--unsigned` skips signing for one build.

## TestFlight and App Store

Builder uploads builds to App Store Connect and submits them to TestFlight or
App Review through the App Store Connect API, from Windows, Linux or macOS. No
Transporter, `altool` or Xcode is involved, and the API key never leaves your
machine: the CI runner only builds and signs, the upload happens locally from
the IPA in `./dist/`.

You need:

- A paid [Apple Developer Program](https://developer.apple.com/programs/)
  membership and an app record in App Store Connect (My Apps → +) with your
  bundle ID
- An IPA signed with an **Apple Distribution** certificate and an **App Store**
  provisioning profile: `builder signing setup --distribution store` creates
  both, stores them as the `STORE` signing set and writes a `store` build
  profile, or pick those types on the portal in the manual path. An IPA signed
  for development is rejected at upload.
- `builder ios build --profile store` (see [Build Profiles](#build-profiles)):
  a `store` profile builds `Release` and signs with that set; a plain `ios
  build` is Debug and unsigned, which is what the dev commands expect, not
  what you want to ship.
- An App Store Connect API key: App Store Connect → Users and Access →
  Integrations → App Store Connect API → Team Keys. Give it the **App Manager**
  role, note the **Issuer ID** and **Key ID**, and download the
  `AuthKey_<KEYID>.p8` file (Apple offers the download once).

### 1. Save the API key

```bash
builder auth apple --issuer-id 12345678-abcd-... --key-id ABC123DEFG --key AuthKey_ABC123DEFG.p8
```

Flags you leave out are prompted for. Builder verifies the key against App
Store Connect and stores it like the other logins (keychain, or a `0600` file on
Linux/WSL); `builder auth status` shows it and `builder auth logout apple`
removes it. In CI or for a coding agent, set `ASC_ISSUER_ID`, `ASC_KEY_ID` and
either `ASC_PRIVATE_KEY` (the .p8 contents; literal `\n` is fine) or
`ASC_KEY_PATH` instead — they take precedence over the saved login.

### 2. Upload the build

```bash
builder ios build            # produces a signed dist/*.ipa
builder ios upload --wait
```

`upload` reads the bundle ID, version and build number from the newest IPA in
`./dist/` (or `--ipa <path>`), finds the app, uploads the archive in chunks
and, with `--wait`, follows App Store Connect until the build has finished
processing and prints its build ID and TestFlight link. Without `--wait` it
returns as soon as Apple has the file.

Two things Apple checks on every upload:

- **Build numbers must increase.** A second upload with the same
  `CFBundleVersion` for the same version is rejected (`ITMS-90189`), so bump
  it before rebuilding.
- **Export compliance.** A build shows as *Missing Compliance* in TestFlight
  until you say whether it uses non-exempt encryption. If your Info.plist sets
  `ITSAppUsesNonExemptEncryption` to `false`, `upload --wait` answers that
  automatically; otherwise pass `--no-encryption` (here or to `submit`) when
  your app only uses standard iOS encryption.

### 3. Distribute to TestFlight

```bash
builder ios submit --testflight --group "Beta Testers" --notes "New login flow"
```

This takes the newest processed build (or `--build-number N`), sets the *What
to Test* notes and adds it to the named groups (`--group` repeats). Internal
groups get the build immediately; the first external group triggers Apple's
beta review, which Builder submits for you (`--wait` follows the decision). Run
it without `--group` to see the build and the groups the app has.

### 4. Submit to the App Store

```bash
builder ios submit --app-store --release after-approval
```

Builder finds or creates the App Store version matching the IPA's marketing
version (or `--version X.Y.Z`), attaches the build, sets the release type
(`manual` or `after-approval`) and submits it for review. The version's
metadata — description, screenshots, age rating, pricing, privacy — must
already be complete: App Store Connect refuses the submission otherwise and
Builder prints Apple's reasons verbatim. Builder does not manage metadata,
screenshots or in-app purchases; fill them in App Store Connect, or on a Mac
with [asc-cli](https://github.com/tddworks/asc-cli), whose production use of
the `buildUploads` API also proved that the Mac-free upload path works and
served as the reference for Builder's implementation.

## Installing the IPA

Use [MobAI](https://mobai.run) to install your IPA directly on your device. It works with both signed and unsigned builds: an unsigned IPA can be re-signed on install with a free Apple ID (MobAI asks for the account).

## Development on Windows/Linux

Builder supports hot reload for Flutter and React Native on Windows/Linux using [MobAI](https://mobai.run) for iOS device control. This allows you to develop iOS apps without a Mac.

## Flutter Development

### Setup

1. Download and install [MobAI](https://mobai.run/download), then connect your iOS device
2. Build your app:
   ```bash
   builder ios build
   ```
   This creates an IPA in `./dist/`
3. Start development with hot reload:
   ```bash
   builder dev flutter
   ```
   Builder installs the IPA through MobAI and asks whether to re-sign it. Re-signing requires an iCloud account - we highly recommend creating a new one at [icloud.com](https://icloud.com) instead of using your primary account. A re-signed app gets a new bundle ID with a team ID suffix (e.g., `com.example.myapp.TEAMID`); Builder prefills it in the prompt that follows.

### Subsequent Runs

Once the app is installed, skip the install step:
```bash
builder dev flutter --skip-install --bundle-id com.example.myapp.TEAMID
```

### File Watching

By default, `builder dev flutter` watches for Dart file changes and automatically triggers hot reload. When flutter attach connects, it also sends an initial hot restart to ensure your latest code is running.

- **Automatic hot reload**: Edit a `.dart` file and save - hot reload triggers automatically
- **Generated files ignored**: Files like `.g.dart` and `.freezed.dart` are ignored by default
- **Configurable**: Customize watched directories, patterns, and debounce via `builder.json`

To disable file watching:
```bash
builder dev flutter --no-watch
```

To print the `flutter attach` command instead of running it (useful for IDE integration):
```bash
builder dev flutter --no-attach
```

### When to Rebuild

- **Native code changes** (Swift, Objective-C, Podfile, native dependencies): Run `builder ios build` and reinstall
- **Dart code changes only**: No rebuild needed - file watcher triggers hot reload automatically

If you don't see your recent Dart changes after launching, press `R` in the terminal to perform a hot restart.

### Troubleshooting

**App won't launch / connection error**
- Close the app on your device before running `builder dev flutter`
- Reconnect the device (unplug/replug USB)
- Restart MobAI
- Run `builder mobai ping` to verify connection

**"No devices found" error**
- Ensure MobAI is running and device is connected
- Only physical iOS devices are supported (no simulators)

**Hot reload not working**
- Make sure you're using the correct bundle ID (with team ID suffix)
- Try hot restart with `R` key
- Check that MobAI shows the device as connected

**File watcher not triggering**
- Ensure you're editing files in watched directories (default: `lib/`)
- Check if the file matches watch patterns (default: `.dart`)
- Generated files (`.g.dart`, `.freezed.dart`) are ignored by default
- Try running without `--no-watch` flag

## React Native Development

### Setup

1. Download and install [MobAI](https://mobai.run/download), then connect your iOS device
2. Build your app:
   ```bash
   builder ios build
   ```
3. Start development with hot reload:
   ```bash
   builder dev rn
   ```
   This will:
   - Start Metro bundler if not running
   - Install the IPA on your device (with optional re-signing)
   - Launch the app with Metro URL configured automatically

### Subsequent Runs

Once the app is installed:
```bash
builder dev rn --skip-install --bundle-id com.example.myapp.TEAMID
```

### Custom Metro Port

If port 8081 is in use:
```bash
builder dev rn --metro-port 8082
```

### When to Rebuild

- **Native code changes** (Swift, Objective-C, Podfile, native modules): Run `builder ios build` and reinstall
- **JavaScript changes only**: No rebuild needed - Metro handles it automatically

### Troubleshooting

**Metro not starting**
- Ensure Node.js and React Native CLI are installed
- Try starting Metro manually: `npx react-native start`

**App not connecting to Metro**
- Device must be on the same WiFi network as the computer running Metro
- Check that Metro is running and accessible
- Verify the Metro port is correct (default: 8081)
- On WSL with mirrored networking, the Hyper-V firewall blocks the phone from reaching Metro by default; see [Using Builder from WSL](docs/wsl.md#react-native) for the firewall rule

**Hot reload not working**
- Shake device or press `d` in Metro terminal to open dev menu
- Enable "Fast Refresh" in dev menu
- Try reloading with `r` in Metro terminal

## Kotlin Multiplatform Development

KMP iOS apps build and run on a device like any other project, with one
difference: **there is no hot reload.** Shared Kotlin is compiled into a native
framework at build time, so there is no runtime to swap code into — every code
change needs a rebuild.

### Setup

1. Download and install [MobAI](https://mobai.run/download), then connect your iOS device
2. Build your app:
   ```bash
   builder ios build
   ```
3. Install and launch it on the device:
   ```bash
   builder dev kmp
   ```

`builder init` detects Kotlin Multiplatform projects by looking for the
multiplatform Gradle plugin in the root and module build files, and asks which
JDK the CI build should use (default 17):

```json
{
  "kmp": { "jdkVersion": "17" }
}
```

On CI, the iOS app is built with `xcodebuild`, whose run script phase (or
CocoaPods) invokes Gradle to compile the shared framework — which is why the
JDK version matters. Gradle output is cached between builds.

### When to Rebuild

Every change to Kotlin or Swift code needs `builder ios build` followed by
`builder dev kmp` again. Use `--skip-install --bundle-id <id>` to relaunch an
app that is already installed.

### Troubleshooting

**Build fails with "Unsupported class file major version" or a Gradle JDK error**
- The project needs a different JDK than the default: set `kmp.jdkVersion` in `builder.json` to match what the project uses locally

**Build fails with "SDK does not contain 'libarclite'"**
- An old Kotlin/Native version against a newer Xcode; upgrade the Kotlin plugin in Gradle

**App launches then immediately exits**
- Launch with `builder dev kmp --logs` to see the device output

## Build Limits

Free allowances belong to each provider account and depend on the plan and
machine. As published in September 2026: Codemagic personal accounts include
500 macOS M2 minutes per month; Bitrise Hobby includes 300 credits. GitHub has
separate allowances for private repositories and free standard runners for
public repositories. Providers change these, so check the
[current allowance links and switching guidance](docs/providers.md#free-allowances).

## License

[MIT License](LICENSE)
