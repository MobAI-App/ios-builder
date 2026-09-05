# Connect Codemagic and Bitrise to your repository

Use this guide once per project before building with a new provider. Your source
stays in the same GitHub repository. GitHub Actions remains Builder's default,
and all three provider logins can be saved at the same time.

There are three separate setup steps: create a CI app, authorize that provider
to access its GitHub repository, and save your API login in Builder. `builder auth`
only saves the API login. `builder init --provider ...` generates local files and
records an existing app ID; it does not create the app or connect GitHub for you.

## Start here

| Task | Codemagic | Bitrise |
| --- | --- | --- |
| Open your account / create a CI app | [Applications](https://codemagic.io/apps) | [Dashboard](https://app.bitrise.io/dashboard) |
| Official app and repository setup | [Add an application](https://docs.codemagic.io/getting-started/adding-apps/) | [Add a CI project](https://docs.bitrise.io/en/bitrise-ci/getting-started/adding-a-new-project) |
| Create an API token | [Account settings → API token](https://docs.codemagic.io/rest-api/codemagic-rest-api/#authentication) | [Account settings → Security → Personal access tokens](https://docs.bitrise.io/en/bitrise-platform/accounts/personal-access-tokens) |
| Repository configuration | `codemagic.yaml` | `bitrise.yml` |
| Builder app ID | ID following `/app/` in the app URL | Project slug following `/app/` in the project URL |

Start with a GitHub repository containing your app and a working `git push`.
For a new Builder project, run `builder init` to set up GitHub first. Skip that
step if `builder.json` already exists.

Decide which branch will hold your generated workflows. Use `main` after the
files are merged, or your setup branch (for example `ci/macos-providers`) to test
before merging. Substitute that branch in every command below. The files must be
pushed to it before a provider can discover the workflows.

## 1. Create the Codemagic app

1. Sign in to [Codemagic Applications](https://codemagic.io/apps) and choose
   **Add application**. Use your **Personal Account** for the personal free
   macOS allowance; review [current pricing](https://docs.codemagic.io/billing/pricing/).
2. Select GitHub and complete authorization. Install the
   [Codemagic CI/CD GitHub App](https://github.com/apps/codemagic-ci-cd) for the
   account or organization owning the repository and include this repository in
   its access selection. Then select the repository and your project type.
3. Finish adding the application. Use YAML configuration for Builder's workflows.
   Copy the app ID from its URL: in `https://codemagic.io/app/APP_ID`, use `APP_ID`.
4. Open **Account settings → API token**, copy your token, and run:

   ```sh
   builder auth codemagic
   builder init --provider codemagic --app-id APP_ID --branch main
   ```

   Paste the token once at the hidden-input prompt and press Enter. Nothing is
   echoed. Keep the API token out of `builder.json` and command arguments.
5. In the app's environment-variable settings, create a group named `builder`.
   Add the non-secret variable `BUILDER=1` for an unsigned first build. See
   [environment groups](https://docs.codemagic.io/yaml-basic-configuration/configuring-environment-variables/).
   Add signing and MobAI secrets to this group when needed.

If the YAML file is not available yet, follow [Push the files and test](#3-push-the-files-and-test), then return to Codemagic and select that branch. The generated workflows
are `ios-build` and `ios-share`, using the M2 Mac with no automatic push triggers.

## 2. Create the Bitrise CI project

1. Sign in to the [Bitrise Dashboard](https://app.bitrise.io/dashboard), choose
   your workspace, and select **New project → Configure Bitrise CI**. Builder
   needs a CI project. Pick a [plan](https://bitrise.io/pricing) suitable for your
   free-usage goal and choose the desired visibility for build logs/artifacts.
2. Connect GitHub in the repository setup. The GitHub App connection requires
   workspace integration credentials; GitHub OAuth is another supported choice.
   Select the account/organization and the same repository used by Builder.
   Complete any GitHub installation or organization approval requested by the
   wizard. An API token alone does not establish this connection.
3. Select the branch that will contain `bitrise.yml`. For XcodeGen projects whose
   generated `.xcodeproj` is absent from Git, use manual iOS setup if scanning
   cannot identify the app. Complete any repository authentication requested by
   the wizard; public HTTPS cloning does not require an SSH key.
4. Finish creating the CI project and copy its slug from the app URL:
   `https://app.bitrise.io/app/APP_SLUG`. Use the project slug, not a build ID or
   the workspace slug. Create a personal API token in **Account settings →
   Security**, then run:

   ```sh
   builder auth bitrise
   builder init --provider bitrise --app-id APP_SLUG --branch main
   ```

5. After [pushing the generated files](#3-push-the-files-and-test), open the project's workflow
   configuration and select **repository** as the location of `bitrise.yml`.
   Confirm it reads the intended branch and offers `ios-build` and `ios-share`.
   [Configuration-location reference](https://docs.bitrise.io/en/bitrise-ci/api/adding-and-managing-apps#changing-the-location-of-the-apps-bitriseyml-file).
6. In **Stacks & Machines**, select a macOS Xcode stack compatible with your app's
   deployment target (Numbra needs iOS 26). The generated YAML requests
   `g2.mac.medium`; check the actual machine and credit usage of your first run.
   Keep the total timeout at 90 minutes or less, and remove onboarding-generated
   push/PR triggers if you only want builds explicitly dispatched by Builder.

For an app created through the API, also complete the GitHub repository
connection in the dashboard. If switching to repository YAML fails, verify that
connection and the committed configuration branch first. During Numbra setup,
this connection was required before the configuration-location API would accept
repository mode.

## 3. Push the files and test

From your project directory, review and commit the generated configuration:

```sh
git add builder.json codemagic.yaml bitrise.yml .builder/ci/runner.sh
git commit -m "Configure macOS build providers"
git push
builder auth status
builder ios build --provider codemagic --unsigned
builder ios build --provider bitrise --unsigned
```

Use the `git add` command after setting up both providers; omit a provider's YAML
if you are setting up only one. Make sure `git push` targets the branch passed to
`--branch`. These workflows expect the snapshot inputs supplied by Builder, so
start the first test through the CLI.

A successful build downloads an IPA and prints its workflow URL. Unsigned IPAs
cannot install directly on an iPhone. Verify the actual runner and remaining
allowance in each provider's dashboard; Builder does not pool quotas or enforce
a spending cap. See [allowances](providers.md#free-allowances).

Once the setup branch merges, change each provider's `branch` in `builder.json`
to `main` and commit that change before deleting the setup branch. Also update
Bitrise's default branch if it points to the setup branch. Do not rerun `init`
over customized YAML without reviewing it: generated files are replaced.

Adding providers leaves GitHub as the default. Use `--provider` for individual
builds; only pass `--set-default` when you intentionally want to change the
default for commands without a provider flag.

## Signing and simulator sharing

Configure signing separately on each new provider. GitHub's saved secrets are
not transferred by these commands.

| Secret | Purpose |
| --- | --- |
| `IOS_CERTIFICATE` | Base64 P12 signing certificate |
| `IOS_CERTIFICATE_PASSWORD` | P12 password, which may be empty |
| `IOS_PROVISIONING_PROFILE` | Base64 provisioning profile matching the app |
| `MOBAI_API_KEY` | MobAI simulator sharing |

Use Codemagic's `builder` group and Bitrise's project Secrets settings. Then
follow [signing](providers.md#signing) or [simulator sharing](providers.md#simulator-sessions).

## Common setup problems

| Symptom | Check |
| --- | --- |
| Logged in, but the repository is missing | Connect the provider's GitHub integration and grant it access to the specific repository; check organization approval. |
| Wrong/missing workflow | Push the YAML and shared runner to the configured branch; enable repository YAML. |
| Bitrise still uses the dashboard workflow | Repository authorization and configuration location are separate settings; complete both. |
| Codemagic cannot find the `builder` group | Create the group for this app/account and add `BUILDER=1` or the required secrets. |
| Bitrise requests an SSH key for a public HTTPS repo | Regenerate/update Builder's YAML so SSH activation is conditional on an actual key. |
| Code signing fails | Install the signing secrets on this provider, or use `--unsigned` for the first test. |
| SDK or simulator is missing | Select a stack/Xcode version supporting the app's iOS deployment target. |
