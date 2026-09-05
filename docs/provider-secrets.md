# Signing and MobAI secrets for Codemagic and Bitrise

Add secrets to each provider app connected to your repository. Builder's local
API login, the provider's GitHub connection, and build secrets are separate:
`builder auth` does not upload signing files or your MobAI key.

| What you want to run | Secrets needed |
| --- | --- |
| Unsigned IPA build (`ios build --unsigned`) | None of the secrets below |
| Signed iPhone build (`ios build`) | All three `IOS_*` secrets below |
| Shared simulator (`ios share`) | `MOBAI_API_KEY`; no Apple signing files needed |

`builder signing setup` uploads secrets to **GitHub Actions only**. For the two
new providers, use the steps below even if GitHub signing/sharing already works.
Existing GitHub secret values cannot be downloaded for copying to another service.

## 1. Prepare your signing files

Builder's generated provider runner exports development-signed IPAs. Prepare:

- An **Apple Development** certificate in a `.p12` file, including its matching
  private key, and the P12 password.
- An **iOS App Development** `.mobileprovision` profile for the same Apple team,
  app bundle ID, certificate, and registered test devices. For Numbra, the bundle
  ID is `run.mobai.numbra`.

Use [Apple Certificates](https://developer.apple.com/account/resources/certificates/list)
and [Apple Profiles](https://developer.apple.com/account/resources/profiles/list).
Apple's [development profile guide](https://developer.apple.com/help/account/provisioning-profiles/create-a-development-provisioning-profile)
explains selecting the App ID, certificate, and devices. App Store/Ad Hoc export
requires a corresponding change to the generated runner's export settings.

If you already have the P12 and profile, reuse them. If you have no certificate,
follow Builder's [certificate creation instructions](../README.md#1-create-a-certificate-signing-request).
Run the CSR/P12 commands in a private directory outside your source checkout:

```sh
builder signing csr
# Upload the CSR in Apple's Certificates page, then download development.cer.
builder signing p12 --certificate development.cer --key ios-signing.key
```

The second command prompts for the P12 password. Use that exact password below.
A `.cer` alone is not the value for `IOS_CERTIFICATE`; assemble the P12 first.
Keep private keys, P12 files, and encoded copies out of Git and build snapshots.

## 2. Prepare the secret values

Use these exact, case-sensitive names:

| Secret name | Value to paste |
| --- | --- |
| `IOS_CERTIFICATE` | Base64 contents of `ios-signing.p12` |
| `IOS_CERTIFICATE_PASSWORD` | The original P12 password, as plain text |
| `IOS_PROVISIONING_PROFILE` | Base64 contents of the `.mobileprovision` file |
| `MOBAI_API_KEY` | The original API key copied from MobAI, as plain text |

Base64-encode only the two files. Paste their contents, not their filenames or
paths. On macOS, copy one encoded file to the clipboard at a time:

```sh
openssl base64 -A -in /path/to/ios-signing.p12 | pbcopy
# Paste into IOS_CERTIFICATE in the provider dashboard before copying the profile.
openssl base64 -A -in /path/to/Numbra.mobileprovision | pbcopy
# Paste into IOS_PROVISIONING_PROFILE.
```

On Linux with `xclip` installed, replace `pbcopy` with
`xclip -selection clipboard`. On Windows, use PowerShell:

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes('C:\signing\ios-signing.p12')) | Set-Clipboard
# Paste into IOS_CERTIFICATE, then encode and paste the profile.
[Convert]::ToBase64String([IO.File]::ReadAllBytes('C:\signing\Numbra.mobileprovision')) | Set-Clipboard
```

The password variable must exist, even for a P12 with an empty password. If the
provider UI does not accept an empty secret, create a password-protected P12 and
use its password. Do not put quotes around passwords or API keys in the value
field, and do not base64-encode them.

## 3. Create the MobAI API key

Open the [MobAI app](https://mobai.run), go to **Account → API Keys**, create an
API key, and copy its value. Save it under `MOBAI_API_KEY` in each provider using
the next two sections. MobAI Pro is required for CI simulator sharing.

This is your MobAI account key. It is separate from the Codemagic/Bitrise API
tokens used by `builder auth`, and from a host's `MOBAI_TOKEN`. If you only want
simulator sharing, you can skip the three Apple signing secrets.

## 4. Add secrets to Codemagic

1. Open [Codemagic Applications](https://codemagic.io/apps), select your app,
   and open **Environment variables** in its settings.
2. Add each required name/value from the table above to the variable group
   **`builder`**. Select **Secret** for each and save with **Add**. Update an
   existing variable instead of creating a conflicting copy.
3. Confirm the group belongs to this app or is accessible to it. Builder's
   `ios-build` and `ios-share` workflows already import it:

   ```yaml
   environment:
     groups:
       - builder
   ```

The non-secret `BUILDER=1` variable used for unsigned onboarding may remain in
this group. Use these environment variables with Builder's generated runner;
uploading files only to a separate code-signing integration does not populate
the `IOS_*` variables it expects. See [Codemagic's environment-group instructions](https://docs.codemagic.io/yaml-basic-configuration/configuring-environment-variables/).

## 5. Add secrets to Bitrise

1. Open the [Bitrise Dashboard](https://app.bitrise.io/dashboard), select your
   CI project, click **Workflows**, and select **Secrets**.
2. Add each required name/value from the table and save it as a project secret.
   Use **Secrets**, not ordinary variables in the committed `bitrise.yml`.
3. Leave **Replace variables in inputs** off for these literal values, where
   that option is available, so a `$` in a password is preserved. Leave
   **Expose for Pull Requests** off; Builder dispatches these workflows itself.

No YAML values need to be pasted into the workflow. Bitrise supplies the secrets
as environment variables. Uploading the certificate/profile only to a separate
code-signing file store does not set the `IOS_*` variables used by this runner.
See [Bitrise's Secrets instructions](https://docs.bitrise.io/en/bitrise-ci/configure-builds/secrets).

## 6. Verify a signed build

In your existing `builder.json`, set `ios.signing` to `true`, preserving the other
project and provider settings. Numbra already has this enabled. Then run from
the app checkout, **without `--unsigned`**:

```sh
builder ios build --provider codemagic
builder ios build --provider bitrise
```

A successful run should archive, export, and download an IPA. Install it on a
device included in the development profile to verify signing and provisioning.
If signing fails, check the P12 password, certificate/private-key pair, profile
expiration, bundle ID, team, and registered devices in the provider's build log.

## 7. Verify simulator sharing

Test one provider at a time:

```sh
builder ios share --provider codemagic --duration 10m
# Release the simulator when finished, then test the other provider.
builder ios share --provider bitrise --duration 10m
```

The CLI returns **submitted** with a run URL. Wait for the simulator to appear
under **CI Devices** in MobAI; the run must build the app and start its bridge
before it is ready. Release it in MobAI when finished, or cancel using the run
ID printed by the CLI:

```sh
builder ios cancel --provider codemagic --run-id CODEMAGIC_RUN_ID
builder ios cancel --provider bitrise --run-id BITRISE_RUN_ID
```

Run the printed snapshot cleanup command after the job stops. `--duration` is
an idle limit, so active interaction can keep the session running longer, within
the provider's total build timeout. See [session lifecycle](providers.md#simulator-sessions).

If the runner reports a missing `MOBAI_API_KEY`, check the Codemagic group import
or Bitrise Secrets entry. For authentication failures, check that the key is
current and belongs to the intended MobAI Pro account. When rotating a key or
renewing signing files, update the secret values on both providers.
