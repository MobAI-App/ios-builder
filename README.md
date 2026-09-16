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
attached to the run as an artifact.

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
| Expo (managed or ejected) | `ios/` | Yes |
| Flutter | `ios/` | Yes |
| Kotlin Multiplatform | `iosApp/` | Yes |
| Cordova/Ionic | `platforms/ios/` | Yes |

### Expo

A managed Expo project has no `ios/` directory in git. `builder init` detects it
as *Expo (managed)*, still records `"ios": { "path": "ios" }`, and the runner
generates the native project with `expo prebuild --platform ios --no-install`
before building it. Ejected projects keep the committed `ios/` they have: the
prebuild step skips a directory that already holds an Xcode project.

`expo prebuild` has to run unattended, so the app config must set the bundle
identifier — `expo.ios.bundleIdentifier` in `app.json`, or `ios.bundleIdentifier`
in `app.config.js` / `app.config.ts`. Without one, prebuild would stop and ask
for it; instead the build fails immediately and names the missing setting.

An `ios/` directory left over from running `expo prebuild` locally is not
uploaded: managed projects gitignore it, and the working-tree snapshot skips
gitignored files. That is what you want — the runner prebuilds from the app
config on every build, so it cannot drift from a stale local copy.

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
builder auth status           # Show which providers you are signed in to
builder auth logout [name]    # Remove stored credentials
builder init                  # Set up workflows in current repo
builder update                # Update builder to the latest release

# Building (builds the working tree, including uncommitted changes)
builder ios build             # Trigger build and download IPA to ./dist/
builder ios build --unsigned  # Build without code signing (if signing is configured)
builder ios build --provider codemagic  # Build on another provider (also: bitrise)

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

# Code signing
builder signing csr           # Create a private key + certificate signing request
builder signing p12           # Assemble a .p12 from the key and Apple's certificate
builder signing setup         # Upload code signing secrets to GitHub
```

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
    "signing": true,
    "configuration": "Debug"
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
| `ios.signing` | Sign the IPA with the uploaded certificate and profile | `false` |
| `ios.configuration` | Xcode build configuration. **Builds are `Debug` unless you set `Release`**; Debug is faster and is what the dev commands expect | `Debug` |

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

For Codemagic and Bitrise, follow the [signing and MobAI secrets guide](docs/provider-secrets.md)
for dashboard instructions, file encoding, and verification. The `signing setup`
command below uploads to GitHub Actions only.

By default, builds are unsigned. Signed builds need a signing certificate and a
provisioning profile — and despite what many guides claim, **you do not need a
Mac to create either one**. The `.p12` certificate is normally created through
Keychain Access, but Builder does the same thing itself: it generates the
private key and certificate signing request, and assembles the `.p12` from the
certificate Apple issues.

You need a paid [Apple Developer Program](https://developer.apple.com/programs/)
membership — the portal only issues certificates to paid accounts. (Without one,
build unsigned and let [MobAI](https://mobai.run) re-sign on install with a free
Apple ID.)

### 1. Create a certificate signing request

```bash
builder signing csr
```

This asks for your name and email and writes two files to the current
directory: `ios-signing.key` (your private key) and `ios-signing.csr`. Keep
the key wherever suits you — just don't commit it (add it to `.gitignore`;
gitignored files are also excluded from build snapshots).

### 2. Create the certificate

1. Go to [Certificates](https://developer.apple.com/account/resources/certificates/add) on the Apple Developer portal
2. Choose **Apple Development** (installs on registered devices) or **Apple Distribution** (App Store/Ad Hoc)
3. Upload `ios-signing.csr` and download the resulting `.cer` file

### 3. Assemble the .p12

```bash
builder signing p12 --certificate development.cer --key ios-signing.key
```

This combines the key and certificate into `ios-signing.p12`, protected by a
password you choose — byte-for-byte the same kind of file Keychain Access
exports, and usable anywhere one is: `builder signing setup`, Sideloadly,
AltStore, or importing it on a Mac. Keep it, and don't commit it.

### 4. Create a provisioning profile

On the portal:

1. **Identifiers** → register an App ID matching your app's bundle identifier
2. **Devices** → register your device's UDID (shown in [MobAI](https://mobai.run) when the device is connected; on Windows, iTunes shows it when you click the serial number on the device page)
3. **Profiles** → create an **iOS App Development** (or Ad Hoc) profile, select your App ID, certificate, and devices, then download the `.mobileprovision` file

### 5. Upload the signing secrets

```bash
builder signing setup --certificate ios-signing.p12 --profile MyApp.mobileprovision
```

This uploads the signing material to GitHub Secrets:
- `IOS_CERTIFICATE` - Base64-encoded .p12 file
- `IOS_CERTIFICATE_PASSWORD` - Certificate password
- `IOS_PROVISIONING_PROFILE` - Base64-encoded .mobileprovision file

You can also skip step 3 and hand `setup` the `.cer` together with the key —
`builder signing setup --certificate development.cer --key ios-signing.key
--profile MyApp.mobileprovision` — and it assembles the `.p12` on the way.

`setup` also sets `ios.signing` to `true` in `builder.json`, which is what
tells `builder ios build` to sign. From then on builds produce signed IPAs; use
`--unsigned` to skip signing for one build. For Codemagic and Bitrise, add the
secrets by hand as described in the
[signing and MobAI secrets guide](docs/provider-secrets.md), then set
`ios.signing` to `true` yourself.

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
