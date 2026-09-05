# macOS CI providers

## Goal

Allow one Builder project to use GitHub Actions, Codemagic personal, and Bitrise
Hobby accounts. The target is roughly 800 macOS minutes per month, subject to
each account's actual machine rates and allowances, never a guaranteed pool.

## Implementation plan

1. Add an explicit provider selection (`github`, `codemagic`, `bitrise`) to
   configuration and `init`/`ios build`/`ios share`. Existing configuration and
   public Go constructors continue to use GitHub. Store app/workflow identifiers
   in builder.json; keep simultaneous, independent saved logins for all three
   providers using keychain storage and the existing headless file fallback.
   Environment overrides are also supported for the new providers.
2. Introduce a small CI provider contract for dispatch, status inspection,
   artifacts, and cancellation. Implement the two REST adapters against official
   API documentation, normalize terminal states, preserve provider run identifiers
   as strings, and keep credentials out of artifact-host requests and error text.
3. Retain GitHub-hosted source repositories and the existing working-tree snapshot
   refs. Trigger the configured provider on a configured repository branch, pass
   the snapshot ref as an environment input, and explicitly fetch it before
   building. Validate provider configuration before pushing any snapshot. Keep
   existing GitHub behavior and API compatibility.
4. Ship Codemagic and Bitrise workflow templates plus shared runner scripts for
   IPA builds and simulator sharing. Preserve framework detection (native,
   Flutter, React Native/Expo, KMP), scheme/product selection, signing, and build
   options. Use free-plan-compatible macOS machines and bounded job durations.
   New provider setup must preserve existing config and refuse to overwrite
   unrelated CI files. Document registering the same repository with each
   provider, API tokens, YAML configuration, and signing/MobAI secrets.
5. Wire selection into build/share coordination, progress, artifact download,
   cancellation on interrupt/timeout, and snapshot cleanup. Detect failed jobs
   and successful jobs missing artifacts promptly. New-provider sharing returns
   a submitted session, not a readiness claim, and provides a workflow URL,
   cancellation command, source-ref cleanup command, and provider session limits.
   GitHub retains its existing waiting behavior.
6. Allow users to switch accounts explicitly to spend their separate free
   allowances. Do not claim an enforceable unified free budget: local accounting
   cannot observe other CI activity, and the providers have different billing
   cycles. Document disabling paid overages at the provider. Automatic quota
   discovery/fallback is deferred until authoritative account usage is available;
   never retry an ambiguous dispatch on another provider and create duplicate
   chargeable builds.
7. Validate config/backward compatibility, REST payloads/auth/statuses,
   artifact downloads, cancellation, template generation, snapshot lifecycle,
   and CLI selection using local tests. Run gofmt, go test -race ./..., go vet,
   and cross-platform builds. Validate runner script syntax and workflow YAML.
   Run live CI smoke builds only if configured accounts/projects are available;
   otherwise describe that validation limit in the PR.
8. Ask Claude Fable 5.1 at high effort to review this plan before implementation,
   then review implementation decisions while it is being developed and address its
   actionable findings. Commit on feat/macos-ci-providers, push, and open a PR
   with the final behavior, setup steps, and validation results.

## Review focus

Snapshot fetching and workflow provenance; remote cancellation after local
timeouts; artifact authentication/redirects; never trusting local usage as a
billing guarantee; simulator readiness signals across providers; maintaining
existing GitHub and Go library behavior; avoiding workflow/script divergence.

## API verification

- Codemagic dispatch/cancel: https://docs.codemagic.io/rest-api/builds/
- Codemagic status and signed artifact URLs: the public v3 schema at
  https://codemagic.io/api/v3/schema/openapi.json (`data.status`,
  `data.artifacts[].short_lived_download_url`). A successful build is `finished`.
- Bitrise: https://api-docs.bitrise.io/docs/swagger.json. Trigger responses can
  contain `results[].build_slug` in addition to deprecated top-level fields.
  Runtime input values require `is_expand: false` so shell-like text stays literal.
- Provider configurations run from a committed branch; app source is fetched
  separately from the exact snapshot ref. No credential is passed in dispatch
  inputs. Providers must already have repository access.
- Public plan allowances and machine rates change. Setup documentation links to
  billing pages rather than hard-coding an automatic 800-minute budget.

## Review outcomes

Claude was invoked twice with `claude -p --model claude-fable-5-1 --effort high`:
for this plan and for an implementation design checkpoint. Both reviews used
written design material and disabled repository tools. Automatic approval review
blocked granting Claude general source-file access; source was reviewed locally.

Adopted findings: verify snapshot SHA, retain refs after ambiguous dispatch or
unconfirmed cancellation, use a bounded fresh context to cancel, preserve old
constructors/defaults, refuse unrelated YAML overwrites, explicitly disable
Bitrise input expansion, separate artifact authentication, validate exactly one
top-level app in downloaded IPAs, tighten credential-file permissions, warn about
environment overrides, and report submitted simulator sessions honestly.

Some model claims conflicted with the fetched primary sources: Bitrise's pricing
page states a 90-minute Hobby build timeout; Codemagic v3 explicitly provides
short-lived download URLs and a documented status enum. Implementation follows
those schemas/pages, rather than guessed historical limits or status names.

Scope decisions: keep GitHub's established coordinator path for compatibility,
share runner scripts between the two new providers, require committed CI scripts,
and use explicit provider selection rather than estimated-budget failover.

## Validation

Completed locally: `go test -race ./...`, `go vet ./...`, the repository-pinned
golangci-lint 2.12.2, `go mod tidy -diff`, Windows amd64/Linux amd64/macOS arm64
builds, YAML parsing, Bash syntax, and a native runner execution with stubbed
Xcode commands under macOS Bash (including paths/spelling requiring quoting).
Tests also exercise independent logins, setup preservation, provider payloads,
status handling, artifact redirects/downloads, and real Git snapshot lifecycle.

Live Codemagic/Bitrise builds and MobAI sessions were not run: this repository
does not contain configured provider apps or an iOS smoke-test project. The PR
must retain this validation limitation until those integrations are exercised.


### Numbra onboarding: token prompt fix

A user reported repeated masked prompt redraws while pasting a Bitrise token.
Provider login now displays one static prompt and accepts hidden input with
`x/term`, with terminal mode changes owned by the calling goroutine. Cancellation
restores the terminal before returning; the CLI exits if input is canceled.

Claude Fable 5.1 at high effort reviewed the written fix design (no source or
credentials were sent). Its cancellation-race concern led to establishing raw
mode before starting the reader; the reader never changes OS terminal settings.
Unix PTY regression tests cover a 330-character paste in a 40-column terminal,
empty input, Ctrl+C, Ctrl+D, SIGINT, no token echo/redraw, and restored terminal
settings. Full race tests, vet, and lint passed locally.
