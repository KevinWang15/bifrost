# Internal fork: 1.6.6-jq.8 to 2.0.0-jq.1

> History consolidated on 2026-09-09. Commit and image identities below record the original validation history. See [the current commit mapping](rebase-history.md); application and test file contents are unchanged.

The candidate is based on the released `transports/v2.0.0` commit
`e4a30d6041c0446603aea615bc5da340dac001b1`. The internal 1.6.6 stack was
rebased in an isolated worktree; the old main remains recoverable.

## Patch decisions

Retain audit encryption and encrypted-log UI, object-only payload storage,
model image-capability validation, SGL structured-tool support, OpenRouter
cache control, explicit Responses stream flags, local Kimi-K3 fallback,
namespace identity/round trips, current Codex detection, and incomplete
Bedrock/Anthropic stream termination. Also retain internal build and workflow
instructions.

Drop the old additional_tools preservation patch: upstream has the implementation
and its round-trip tests pass. Drop the explicit CI registration of the capability
validator: upstream discovers all plugin modules. Keep one copy of the duplicated
image target. Replace old version-only increments with `jq.1` on the new base.

Conflicts preserve upstream request-local compatibility state, expanded client
identities, session-header tests, accessible UI controls, grouped-log columns,
new logging initializer arguments, and current module versions. The incomplete
stream test still fails on unmodified 2.0, so the internal terminal-event fix stays.

## Upstream regression found during rehearsal

Claude Code through OpenRouter's OpenAI models passed on exact jq.8 but failed on
stock 2.0 after a successful tool result with invalid_encrypted_content / item_id
mismatch. Extend upstream's reasoning-item ID preservation to OpenRouter's OpenAI
models, including custom providers based on OpenRouter. Other model families keep
their existing signature treatment. Streaming and non-streaming round-trip
regressions and an HTTP provider-harness folder cover this correction.

## New billing integration

2.0 hydrates offloaded usage for cost recalculation and caches small billing
payloads in SQL. Object-only rows must recover the complete cache breakdown but
must never enter that backfill set. The generic store honors the transient
object-only policy; the encryption decorator restores it from the persisted v2
summary marker. This read policy stays active if encryption of new requests is
disabled. SQL-only deployments continue to store the encrypted carrier in SQL.

The v2 marker also survives new object-only writes whose content is hidden;
serving reads still honor `content_hidden`. Legacy hidden rows without a persisted
v2 marker follow upstream's hidden-row billing policy.

`video_edit_input` and `guardrail_debug` follow the existing explicitly accepted
cleartext auxiliary-payload policy. Four conversation fields remain encrypted,
and raw request/response bodies are cleared. This change does not expand the
scope of encrypted content to every auxiliary field.

## Upgrade and restore

1. Record image/source IDs and retain the old image. Quiesce writers and back up
   Bifrost config/log databases, Portal database, configuration, encryption key,
   and any object data as a coherent set. Keep sensitive artifacts outside Git.
2. Rehearse on restored, isolated copies with the old image first. Include owned
   virtual keys, encrypted/exempt rows, cached usage, OAuth tokens and a pending
   user OAuth flow. Verify decryption before upgrading.
3. Start 2.0 against its dedicated restored database. Check migration completion,
   historical content/cost/usage, identities, OAuth credentials, authorization,
   inference, and new writes. Restart and verify idempotence.
4. Roll back by restoring the pre-upgrade databases/configuration and starting
   the old image. An image-only rollback is not a supported recovery procedure.

2.0 merges MCP OAuth token/flow tables and drops five columns from oauth_configs:
state, code_verifier, code_challenge, expires_at and token_id. Pending admin OAuth
flows stored only on the old config row must be initiated again. Pending user
flows have a migration path. Do not share a migrated schema between old and new
writers in a rolling deployment.

Other 2.0 changes: native plugin download URLs on private networks require the
server allowlist; custom-path plugin API writes require admin authentication;
use canonical governance routes and their pagination contract; plugin PreHook now
runs after authentication, with PreAuthHook available for the earlier phase;
request cost breakdown uses input/output/additional categories. See the upstream
v2.0.0 migration guide for details.

## Local validation

Use Go 1.27.0. Run `GOWORK=off go test -mod=readonly ./bifrost-http/server` in
transports, relevant provider/schema/MCP suites, logstore, logging, encryption and
capability-validator tests. Build the Bifrost UI before standalone type checking
so Vite generates route types. Run its Vitest suite.

Run Portal backend pytest, UI type/build checks, browser audit regressions
(`CHROME_BIN` selects an available Chromium), and Go/browser envelope interop.
Verify real PostgreSQL and S3 behavior, including repeated cost recalculation
without object-only payload backfill. Distinguish deterministic providers from
live-provider and CLI tests in the handoff.

## Commit decisions and test evidence

The [HTML rebase report](bifrost-v2-rebase-report.html) records every original
commit's disposition, replay SHA, rationale and specific validation. It also
covers the two integration fixes, migration/restore rehearsal and accepted
provider coverage limits. The [evidence index](bifrost-v2-rebase-evidence.json)
contains sanitized test outcomes and artifact hashes; private fixtures and
backups remain on the rehearsal workstation.

## Proposed production rollout

The report now includes an [upgrade plan](bifrost-v2-rebase-report.html#plan)
with owners, staged actions, exit gates, full backup coverage, acceptance checks
and recovery decisions. Local rebase and rehearsal are complete; production
publication, preparation and rollout remain future work.

The documented internal environment uses single-host Compose. Plan a maintenance
cutover: review/freeze source, verify/publish the exact image, prepare deployment
commands, rehearse at production scale, drain and stop old writers, take and
restore-validate the full backup, migrate with one new instance, verify, reopen
traffic, and observe before closing the release.

The deploy repo's configuration backup excludes logs and runtime/session data,
so it cannot serve as the complete migration rollback backup. Its ordinary
internal execute command starts several services together; staged cutover gates
must be prepared and rehearsed. For HA targets, prevent old-version failover
until all standbys are upgraded. Rollback after migration requires restoring the
coherent recovery set; after traffic resumes, it also requires accounting for
post-backup writes that the restore discards.
