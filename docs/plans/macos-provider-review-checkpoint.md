# Implementation review checkpoint

Please review these implementation decisions, without repository/source access.
This is the second Claude review requested during implementation. Identify
concrete bugs or missing tests implied by these choices; do not assume unseen code.

- GitHub retains its original coordinator path and constructors. Empty provider
  selection resolves to GitHub. A command override wins over the configured
  default. Codemagic and Bitrise use a small separate REST provider interface.
- Each provider has its own saved token. Codemagic and Bitrise API token logins
  validate a read-only apps endpoint, then store independent keyring entries.
  Linux follows the existing GitHub fallback using separate mode-0600 files.
  Environment token overrides take precedence only for the new providers.
  Logging into a provider does not select it or remove another provider's login.
- Setup adds app ID, workflow IDs, and the branch to existing configuration,
  preserving the other provider accounts and iOS/framework settings. Default
  stays GitHub unless explicitly changed. Unrelated YAML files are not overwritten.
- Provider YAML is loaded from the configured committed branch. The workflow
  copies its shared runner script outside the source tree before fetching the
  snapshot, so both workflow and runner script come from that committed branch.
  Only application sources come from the working-tree snapshot. This mirrors
  GitHub workflow provenance; setup docs explain script edits need committing.
- Each dispatch gets a random build ID, snapshot ref, and expected SHA. The runner
  fetches the full ref, verifies FETCH_HEAD matches the SHA, then checks out
  detached. GitHub app/deploy-key repository access performs the fetch.
- Dispatch requests are never retried. If the response fails or has no run ID,
  the local client leaves the snapshot ref intact and asks the user to inspect
  the dashboard before retrying. There is no automatic provider fallback.
- Codemagic uses its documented legacy dispatch/cancel endpoints and the v3
  status endpoint, whose JSON includes data.status and artifacts with signed
  unauthenticated download URLs. Only the v3 schema's documented terminal states
  are accepted. Bitrise supports both results arrays and old top-level run IDs,
  numeric states 0-4, artifact pagination, and artifact detail download URLs.
- API clients refuse redirects and omit remote response bodies from errors.
  Artifact downloads use a separate unauthenticated HTTPS client and reject
  redirects to HTTP. Downloads stream to a temporary output file, validate size
  when supplied, check the IPA ZIP contains an app Info.plist, then rename.
- IPA builds poll every ten seconds and wait for a terminal state. Failure and
  success-without-an-IPA are explicit errors. If the local operation aborts,
  cancellation uses a fresh context and confirms terminal state before removing
  the snapshot. If confirmation fails, both run URL and retained ref are printed.
- Simulator sharing on the new providers is deliberately asynchronous: it
  returns "submitted", not "ready". The CLI prints the run URL, a cancellation
  command, and a command to delete its snapshot only after the run finishes.
  There is no attempt to infer readiness from a build step. Provider templates
  are bounded to 90 minutes on free accounts; local idle duration is limited
  to 60 minutes and documented as distinct from total runtime. The existing
  GitHub share path continues to wait as before.
- The common runner supports native, Flutter, React Native/Expo and KMP, uses
  quoted Bash arrays for xcodebuild, preserves xcconfig-template materialization,
  scheme/product selection, and manual signing, and uses mobai-ci standalone
  installation, sim boot, and share commands.
- Automatic usage routing is deferred. Setup explains independent account pools,
  changing rates, personal/Hobby eligibility, and provider-side overage controls.
- Planned local tests cover credential independence and file permissions,
  backwards-compatible default selection, API payload/status/error fixtures,
  unauthenticated artifact redirects, downloads, cancellation, snapshot SHA
  verification in a bare test repository, YAML validity, CLI setup preservation,
  and shell syntax. Live CI smoke depends on configured accounts being available.
