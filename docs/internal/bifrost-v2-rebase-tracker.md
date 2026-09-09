# Bifrost 2.0 internal-fork rebase and migration rehearsal

> History consolidated on 2026-09-09. Commit and image identities below record the original validation history. See [the current commit mapping](rebase-history.md); application and test file contents are unchanged.

## Objective and authorization

Rebuild the internal Bifrost patch stack on released upstream `transports/v2.0.0`, understand and preserve each internal behavior still needed, and drop patches only when upstream equivalence is demonstrated. Update the local development environment and verify upstream, internal functionality, and migration from the current 1.6.6 deployment. Target internal version: `v2.0.0-jq.1`.

User explicitly requested a rebase and this local Markdown tracker. Those instructions supersede the older repository/skill guidance to merge tags and keep tracking only in GitLab. This work produces a local release candidate and evidence; production rollout and registry publishing are outside this development/rehearsal phase.

## Status

- Phase: local rebase, migration/restore rehearsal, and final local cutover complete. The user confirmed that Bedrock, SGL and Kimi were already broken; these pre-existing provider issues are accepted coverage limits and do not block this upgrade. This is a locally verified candidate, not a production rollout.
- Verified implementation commit: `8654f269a163c4f7795a85d0ca89f0804d4a6c78`; 16 replayed patches plus two tested 2.0 adaptations. Original main and user changes preserved; local stack updated.
- Repository tracker: `docs/internal/bifrost-v2-rebase-tracker.md`, checked into the rebase feature branch at the user's request. Originally created before implementation at `/root/dev/bifrost-v2-rebase-tracker.md` (`~/dev` in this Codex session), also linked at `/home/ke-wang/dev/bifrost-v2-rebase-tracker.md`. Local evidence paths below refer to the rehearsal workstation. See [migration notes](v2-upgrade.md) for the implementation and recovery procedure.
- Source repository: `/home/ke-wang/dev/bifrost`.
- Original fork preserved: `main`, `0b81a4bab`, version `1.6.6` / `jq.8`.
- Upstream base: `transports/v1.6.6`, `a15edc24a8656950a10c6c3c60430b913cfb313b`.
- Target upstream: `transports/v2.0.0`, `e4a30d6041c0446603aea615bc5da340dac001b1`.
- Candidate branch: `feature/upstream-v2.0.0-rebase`.
- Candidate worktree: `/home/ke-wang/dev/bifrost-v2-rebase`.
- Upstream control worktree: `/home/ke-wang/dev/bifrost-v2-upstream`.
- Preserve the existing uncommitted `.dockerignore` changes in the original checkout.
- Gateway checkout: `/home/ke-wang/dev/llm-gateway`; currently `chore/portal-version-0.8.7`.
- Local Bifrost container: `deploy-bifrost-1`, image `bifrost-company:local-v2-jq1-8654f269a`, host port 14080; healthy, zero restarts. Original jq.8 image retained.
- Existing Compose project `deploy` uses `docker-compose.dev.yaml` plus `docker-compose.reporting.yaml`; preserve both overlays and other local services.
- Separate `gateway-observability` project is also running.

## Execution plan and acceptance gates

### 1. Freeze and inventory

- [x] Identify current source refs, working-tree changes, and running local services.
- [x] Save immutable backup refs and record exact full SHAs and image IDs.
- [x] Read applicable repository instructions and relevant local test/deployment configuration.
- [x] Inventory local volumes, DB engines/schema versions, object-store backend, image build paths, credentials availability (names only), ports, and existing test harnesses.
- [x] Establish a baseline against the running 1.6.6-jq.8 application before changing it.
- [x] Capture reusable, non-secret test fixtures and baseline assertions. Include encrypted and exempt audit rows, usage/cached tokens, teams, virtual keys, budgets, aliases and MCP configuration where applicable.

Gate: original source/worktree and local data are recoverable; actual local topology and test inputs are documented.

### 2. Understand and classify every internal commit

For each commit below, read its complete diff, relevant surrounding upstream code, tests, and follow-up fixes. Record its user-visible purpose and classify as KEEP, ADAPT, DROP-UPSTREAM, or SUPERSEDED-INTERNAL. A matching commit title or a clean textual merge is insufficient evidence of equivalence.

For any dropped fix, record the upstream implementation and a regression that passes on pristine 2.0.0. For retained fixes, where practical show the regression failing on pristine upstream and passing on the candidate. Group follow-up commits into coherent behavior while preserving original SHA attribution in the decision ledger.

| Original commit | Purpose to investigate | Decision / evidence |
|---|---|---|
| `231f8353f` | Internal image publishing target | ADAPT → `378b4f61c`; retain internal publishing target and upstream COMPAT/.PHONY/helm-index. |
| `f2f3e0328` | Internal image publishing target follow-up | SUPERSEDED-INTERNAL; same logical build-target change as 231f8353f; inherited .PHONY context differs, but the internal additions/deletions match after accounting for helm-index. |
| `2a156ec70` | Upstream synchronization skill/runbook | ADAPT → `c706e7fce` + `168b77d40`; worktree/path discovery and explicit major-version/local scope documented. |
| `b57e094ab` | Preserve Responses `additional_tools` items | DROP-UPSTREAM; upstream schemas preserve additional_tools and exact round-trip regressions pass (`upstream-additionaltools.log`). |
| `3f847ff17` | Encrypt audit content at storage boundary | ADAPT → `a1aaf9a4a`; preserve encryption at storage boundary, upstream logging initializer arguments. |
| `3eacb7509` | Avoid rendering encrypted audit ciphertext | ADAPT → `6bbcff51b`; encrypted UI plus upstream accessible reveal switch. |
| `f0cf832fe` | Keep encrypted payloads in object storage | ADAPT → `c9cc9e1cc`; upstream payload additions retained. Billing policy adapted/tested in `168b77d40`. |
| `7d7d576b6` | Internal fork development instructions | KEEP → `bd41afcee`; internal development instructions. |
| `07cd82e88` | Model input-image capability validation | KEEP → `1ad38099d`; explicit non-image rejection; unknown capability still permitted. Package tests pass. |
| `226efe6c3` | Forward OpenRouter Responses cache control | KEEP → `133a4c49a`; OpenRouter-only cache_control forwarding; provider regressions pass. |
| `617486747` | Register validator module in CI/workspace | DROP-UPSTREAM; setup-go-workspace.sh now discovers all plugin modules dynamically. |
| `3d0701bb3` | Terminate incomplete Bedrock streams | KEEP → `73bba745b`; pristine 2.0 regression fails (0 vs 2 terminal events), candidate passes. |
| `9c7180747` | SGL structured Responses retain tools | KEEP → `44ea5df9e`; SGL synthetic final-answer tool preserves user tools. Provider regressions pass. |
| `5e105d3c0` | Bridge namespace tools for non-OpenAI providers | ADAPT → `48e89a80f`; namespace codec retained alongside upstream request-local droppedParams fix. |
| `e70f4493a` | Recognize current Codex MCP clients | ADAPT → `5c2c6eb5d`; add current Codex IDs without removing upstream expanded identities/tests. |
| `4db797299` | Explicit Responses stream flag | KEEP → `dfce6f550`; explicit nonstream Responses flag. |
| `e288d859d` | Internal version jq.6 | SUPERSEDED-INTERNAL; version-only, new base is jq.1. |
| `db3f527b7` | Kimi-K3 Responses-to-chat fallback | ADAPT → `c644717ec`; local/local_v2 Kimi-K3 chat fallback retained; set new-base jq.1. |
| `f6ffc1dfb` | Namespace identity and native Codex detection | ADAPT → `61accf4db`; namespace identity/native detection plus upstream session-header tests. |
| `5c2552a3d` | Idempotent namespace restoration | KEEP → `846aa3439`; idempotent tool restoration. Compat suite passes. |
| `4685fe980` | Internal version jq.8 | SUPERSEDED-INTERNAL; version-only, new base is jq.1. |

Gate: all 21 non-merge commits have an explicit disposition, including follow-up dependencies and regression coverage.

### 3. Rebase the patch stack

- [x] Create isolated candidate and pristine-upstream worktrees; protect original refs.
- [x] Replay/rebase selected internal commits onto the exact upstream release commit, preserving logical patch order and original attribution.
- [x] Resolve conflicts by preserving behavior and adopting upstream interfaces, never blanket ours/theirs.
- [x] Review cleanly applied changes as well as textual conflicts; check for semantic duplication and accidentally revived removed code.
- [x] Adapt HTTP plugin interface/pre-auth phase, cost breakdown and log-store signatures, new log payload types, billing paths, CI workspace, and module dependencies.
- [x] Set `transports/internal_version` to `jq.1`; retain upstream's `transports/version` (`2.0.0`).
- [x] Review `range-diff`/final diff and internal-only file inventory against both original fork and upstream. Confirm no unintended provider/API behavior removal.
- [x] Keep the skill's knowledge consistent with actual paths and resulting development workflow where changed.

Gate: candidate is descended from exact upstream 2.0.0, original branch is intact, decisions are traceable, and the patch stack builds.

### 4. Verify pristine upstream and the rebased fork

Record each command, source SHA/image ID, result, duration/log path, and any skipped prerequisites. Distinguish upstream failures from internal regressions. Do not label a mock-only run as live-provider validation.

- [x] Build pristine upstream 2.0.0 and candidate with consistent toolchains.
- [x] Run relevant upstream package suites (schema/conversion, provider regressions, MCP, config/log migrations, governance and transport).
- [x] Run `GOWORK=off go test -mod=readonly ./bifrost-http/server` from candidate transports.
- [x] Run candidate framework logstore and log-encryption suites, including meaningful PostgreSQL/object-store coverage.
- [x] Run retained internal regressions: additional tools, model capabilities, Bedrock incomplete streams, namespace round-trip/idempotence/collisions, Codex detection, Kimi fallback, OpenRouter cache control, SGL structured tools, explicit stream flags.
- [x] Run Bifrost UI checks/build and encrypted-log rendering tests.
- [x] Run Portal backend tests, UI type/build checks, cross-language audit regressions, and compatibility checks for costs/governance/routing.
- [x] Run API and browser end-to-end checks for chat, Responses, Anthropic Messages, streaming/tool-use/error termination, virtual-key isolation, budgets, logs, aliases, login/ownership and authorized audit decryption.
- [x] Run available configured live-provider scenarios; record any unsupported/unavailable credentials or provider outages as explicit coverage gaps.

Gate: meaningful upstream and internal behaviors pass, or a specific upstream defect has a documented tested remedy. No hidden failed/skipped release gates.

### 5. Rehearse migration and rollback on isolated local data

- [x] Back up local config DB, logs DB, Portal DB and applicable object-store contents/configuration coherently; protect artifacts outside Git and never print secrets.
- [x] Restore backups into isolated rehearsal resources with unique project/container names and ports. Use only local development data or synthetic fixtures.
- [x] Boot exact 1.6.6-jq.8 on restored data; verify baseline counts, ownership, usage, keys, ciphertext/object references and sample inference.
- [x] Exercise stock upstream 2.0.0 on a separate suitable baseline copy to identify upstream migration behavior independently; do not expect stock upstream to implement internal encryption.
- [x] Upgrade the internal baseline copy to the rebased 2.0.0-jq.1 candidate; record migration IDs, duration, logs and resulting schema.
- [x] Validate existing data, encryption envelopes v1/v2, object-only confidentiality, historic costs/cached usage, permissions, budgets, MCP configuration, and Portal behavior; create fresh records and compare.
- [x] Restart candidate to prove migrations are idempotent and service state persists.
- [x] Rehearse backup restoration and boot old 1.6.6-jq.8; confirm baseline behavior returns.
- [x] Explicitly assess mixed-version operation: 2.0 drops OAuth columns, so do not assume an image-only rollback or shared-schema rolling upgrade is safe.

Gate: measured upgrade and restore paths work with representative fixtures, with data integrity and rollback limitations recorded.

### 6. Update and verify the local development stack

- [x] Apply candidate image/config changes to the existing local test environment after rehearsal succeeds; preserve existing reporting/observability overlays.
- [x] Make any necessary Portal development compatibility changes on an isolated branch/worktree.
- [x] Run end-to-end checks through the local stack's real network/auth/DB/object-store paths.
- [x] Record final local URLs, image tags/IDs, source SHAs, retained backups, test commands and observed limitations.
- [x] Produce a final decision ledger and release/migration handoff; identify any remaining blockers honestly.

## Known risks from initial investigation

- Preliminary merge preview (not the requested rebase) found 13 conflicts; individual rebase commits may have a different conflict count.
- Upstream 2.0 changes cost structure, HTTP pre-auth hooks, governance APIs, plugin download/auth behavior, billing/log storage and OAuth schema.
- Encryption integration must cover newly introduced payload fields and write/backfill paths, not just preserve existing symbol names.
- Existing local stack has reporting and observability services beyond the README's minimal Compose example.
- Existing upstream synchronization skill contains old paths/remotes and cannot be executed verbatim.

## Evidence log

| Step | Result | Evidence / artifact |
|---|---|---|
| Initial source inventory | Complete | Original `main` at `0b81a4bab`; only `.dockerignore` modified |
| Initial internal history inventory | Complete | 21 non-merge commits after upstream 1.6.6 |
| Initial local runtime inventory | Complete | `deploy` project (13 services) plus `gateway-observability` (4 services) |
| Planning tracker | Created before rebase/environment mutations | This file |

## Resume notes

Execution complete. Start with the final results and coverage limits below. Earlier evidence entries describe intermediate states; they do not override the final source/image status.

### Baseline evidence (2026-09-08)

- Backup branch: `backup/main-pre-v2-rebase-20260908` at `0b81a4bab3f55ad510749060739a6f4c884b4843`.
- Running image ID: `sha256:af0481ec7433ea41250bbbc764da06ae1dbd58870f77b25a4b2dbfe81f2e814c`.
- Health, teams, virtual keys, providers, logs and MCP APIs returned 200. Snapshots and local PostgreSQL dump saved privately under `/root/dev/bifrost-v2-evidence`; no secrets in this tracker.
- Config and log stores use PostgreSQL; databases include `bifrost` and `portal`. Baseline dump is ~52 MB.
- Prior jq.8 report `/tmp/bifrost-prerelease-jq8/report.md` records schema-test compilation and reasoning replay failures, unavailable live SGL, and Bedrock credential rejection. Recheck these against 2.0.
- Pristine 2.0 build and schema/MCP tests running; host Go 1.27.0 matches target Docker builder.
- Full original commit patches archived for audit and range comparison.

### Rebase and first validation pass

- 16 retained commits replayed onto exact upstream 2.0.0; candidate head `73bba745b`. Original checkout and local running stack unchanged.
- Pristine upstream Docker build passed: `bifrost-upstream:local-v2-e4a30d604`; Go 1.27.0.
- Upstream schema, MCP, configstore, logstore, governance, routing, transport server/lib suites passed.
- Candidate schema, MCP, compat, capability validator, logstore, envelope crypto and targeted provider suites passed. No-workspace transport server gate passed.
- Candidate encryption exhaustive policy test caught new video_edit_input and guardrail_debug payload fields. Their classification is being adapted deliberately under the existing auxiliary-payload policy.
- Semantic audit caught a new billing interaction: object-only rows must ignore DB-resident exclusions for hydration, and must not backfill payloads into SQL. Follow-up regression and fix in progress.

### Candidate build and isolated fixtures

- Candidate Docker image built successfully: `bifrost-company:local-v2-jq1-candidate` (Go 1.27.0, source at 73bba745b plus recorded adaptation diff). Final immutable source/image naming still pending.
- New billing regression passes: object-only rows recover full cached-token usage despite configured SQL exclusions, skip SQL backfill, and fetch once per chunk. Historical v2 policy restored even when new encryption is disabled.
- Internal plugin go.mod/go.sum updated to upstream core 1.8.3/framework 1.6.0 and Go 1.27.0; no-workspace transport gate passes again.
- Bifrost UI build/typecheck + 19 files / 320 tests pass. Direct typecheck before Vite route generation fails; normal build generates required types.
- Portal backend: 242 tests pass. Clean worktree UI type/build, browser audit regressions and Go/browser crypto interop pass. Original ignored .next contained stale deleted-page references; original checkout preserved. Clean Portal branch `feature/bifrost-v2-compat` at 6d3818a has no source changes yet.
- Candidate configstore/governance/routing pass. Broad transport lib run hit existing SQLite database-is-locked in VK/MCP reconciliation; targeted rerun passes. Record this as a flaky validation result, not a clean first pass.
- Real PostgreSQL encryption carrier list/read/decrypt test passes.
- Coherent local backups captured while Bifrost, Portal backend, and cache reporter were briefly paused, then unpaused. App data has only config.json, no existing object-store data. Dumps/config/env artifacts remain private.
- Existing local BF_LOG_ENCRYPTION_ENABLED=false. Isolated candidate copy explicitly enables it for coverage; original live local config is unchanged.
- Rehearsal fixtures: 2 teams (encrypted/exempt), 2 owned keys with budgets, synthetic boss/owner RSA keys, cached usage, shared/VK OAuth credentials, pending user OAuth flow and disabled MCP client. Before migration: 659 logs, 6 virtual keys, 2 teams, 252 migrations. Owner access passes; unrelated user denied; real Go ciphertext decrypts through Portal browser crypto as both owner and boss.
- Preliminary stock 2.0 migration: healthy, 290 migrations (+38), shared/VK tokens and pending user flow preserved. Repeating with final encrypted fixtures before comparative validation.
- Isolated resources use network `bifrost-v2-rehearsal`, API ports 24080 (candidate), 24081 (upstream), Portal 24076, S3 29000. Existing local services retain their original ports/images.

### Migration, rollback, and S3 results

- Candidate head committed as `168b77d40fbd179957f7be9aff564d4990c63808`: 16 replayed internal commits plus one 2.0 adaptation commit. All 30 internal-only files retained; exact upstream tag is an ancestor; original main unchanged.
- Final source image build running as `bifrost-company:local-v2-jq1-168b77d40`.
- Stock 2.0 and candidate both preserve all 659 old log payloads/costs/usage (stable-column aggregate hash), all governance/provider/MCP IDs, and Portal schema/counts. Both migrate 252→290 migration entries.
- Shared OAuth and VK tokens decrypt to their exact synthetic pre-migration values; pending user flow keeps its state/verifier. New tables may encrypt previously plaintext legacy credentials, so ciphertext equality alone is not a valid migration assertion.
- Candidate restart preserves schema/migration set and historic data; owner+boss audit decryption still passes.
- Restore rehearsal: stop stock 2.0, restore seeded pre-upgrade Bifrost+Portal dumps, start exact jq.8 image. Original schema, 252 migrations and all historical payload/cost/usage return. Image-only rollback was not used.
- Separate PostgreSQL+MinIO rehearsal boots jq.8 with object storage enabled and SQL exclusions for token_usage/cache_debug. Old encrypted object hydrates and decrypts before and after upgrade.
- Two real HTTP cost-recalculation jobs each update 2/2 rows with zero skips after a synthetic pricing override. Cached 70 of 100 input tokens plus 10 output tokens produces expected cost 0.000057. Object-only SQL payload columns stay empty after both jobs.
- Full Anthropic/Bedrock/OpenAI/SGL package suites pass. Full transport lib suite passes with serial test scheduling after the earlier SQLite locking flake.

### Local cutover and live checks

- Final image built: `bifrost-company:local-v2-jq1-168b77d40`, image/index ID `sha256:d4217c926ab1786c152ee17cf73a67f964c53b01fb1e1ef58b14f8e59c7c80c3`. Local deploy/.env now references it.
- Fresh cutover backup under evidence/cutover-backup; application writers paused for 1.03s.
- Running Compose labels referenced a reporting overlay absent from this checkout. Recovered its exact content from gateway commit 3c3e2cc into private evidence and included it during the Bifrost-only recreation. Reporting/Grafana/observability containers were not recreated.
- Local cutover successful: 657 historical logs, unchanged payload/cost/usage hash, Portal schema/counts unchanged; 252→290 migrations; health and management APIs pass. Encryption remains disabled in the original local environment, as before; isolated encryption/S3 fixtures remain available.
- Final-image deterministic HTTP tests: image-capability rejection 3/3 (zero upstream requests); namespace collision 4/4 across native/integration Responses and streaming/nonstreaming.
- Canonical core harness passes with `USE_INFISICAL=0` and GOPATH/bin on PATH: 4,147 passed / 33 skipped / 0 failed. Skips recorded in core-skips.json (mostly absent live provider credentials). Initial attempts exposed missing Infisical and gotestsum PATH configuration, corrected without source changes.
- Codex CLI with real providers and SearXNG MCP: 8/8 sessions pass; temporary key removed.
- Claude CLI: 4/5 pass; direct OpenAI gpt-5-mini now passes. OpenRouter gpt-5-mini consistently fails after successful MCP return with invalid_encrypted_content (item_id mismatch); same failure reproduced on stock 2.0. Comparing exact old jq.8 image before classifying it.

### Final results (2026-09-08)

- Final source: `8654f269a163c4f7795a85d0ca89f0804d4a6c78` on `feature/upstream-v2.0.0-rebase`, clean tracked worktree. Version files: `2.0.0` / `jq.1`.
- Exact upstream release commit: `e4a30d6041c0446603aea615bc5da340dac001b1`. Annotated tag object is `9537b2fadf42af90eb34ed47d3d4252e1beff4a0`; these identify the tag and commit respectively, not different source bases.
- Final image: `bifrost-company:local-v2-jq1-8654f269a`, image/index ID `sha256:bf96e10faa8e74ab49facf37744bea59bcc1cd5a8e15bff4140f9ffa03f97f02`. Running on the existing local stack. Only BIFROST_IMAGE changed in the ignored deployment .env; other containers retained.
- All 21 old commits classified: 16 replayed, two upstream-covered changes dropped, one duplicate and two old version bumps superseded. Two follow-up commits implement 2.0 billing integration and fix the discovered upstream OpenRouter reasoning replay regression. Final range-diff reviewed, diff check clean, all 30 old internal-only files retained.
- The OpenRouter defect is demonstrated by the same Claude Code workflow passing on exact jq.8 and failing on stock 2.0 and the initial rebase. OpenRouter's OpenAI models were missing from upstream reasoning-item ID preservation. New streaming/nonstreaming tests fail before the correction and pass after it; the fix keeps non-OpenAI model behavior unchanged.
- Final `make test-core`: **4,155 passed, 33 skipped, 0 failures** (4,188 total). Canonical report and explicit skip list retained. No-workspace transport server test passes on final HEAD.
- Final image: Claude Code **5/5** and Codex **8/8** pass through normal ingress, real providers and SearXNG MCP. Persisted audit: **28/28 successful inference requests, 13 successful searches**, no duplicate bare tool definitions. Gateway-reported cost for this final CLI matrix: **$0.080782929**. All temporary test virtual keys deleted.
- New OpenRouter HTTP harness: **3 requests / 30 assertions pass**, including actual encrypted reasoning capture and replay (no skipped assertions), plus streaming completion and routed identity headers.
- Final image live OpenRouter cache-control HTTP checks: **4/4** across native/integration Responses and streaming/nonstreaming; TEST_OK returned. This verifies acceptance and forwarding, not cache-hit economics. Invalid key returns 401.
- Final image deterministic namespace collision checks: **4/4**. Capability rejection: **3/3**, zero upstream requests. First attempt to launch these two harnesses together collided on their shared mock port; sequential rerun resolves the harness issue.
- Final image PostgreSQL+MinIO: two cost recalculation passes update 2/2 rows, zero skips; the offloaded encrypted fixture retains empty SQL payloads and decrypts through actual Portal crypto as both owner and boss.
- Bifrost UI **320 tests**, Portal backend **242 tests**, both UI builds/types, browser audit regressions and Go/browser crypto interop pass. Real browser Bifrost login, encrypted log rendering, and Portal Keycloak SSO pass on the final image with zero JavaScript errors. Portal required no source changes.
- Migration/rollback evidence remains valid: pristine upstream and fork both preserve 659 seeded historical logs and identities, 252→290 migration entries; candidate restart is idempotent; restore plus exact jq.8 returns original schema/data. Actual local cutover preserves all 657 pre-cutover historical logs/cost/usage and Portal state.
- Local URLs: Bifrost http://localhost:14080, inference ingress http://localhost:13080, Portal http://localhost:13002 / https://localhost:13443, Grafana http://localhost:13001. Bifrost, Portal UI, Grafana health and Prometheus readiness return 200.
- Reviewable source handoff: `/home/ke-wang/dev/bifrost-v2-rebase/docs/internal/v2-upgrade.md`. Source/image/API audit summaries and test logs: `/root/dev/bifrost-v2-evidence`. Fresh pre-cutover dumps/config/env/app-data: `cutover-backup/` within that directory. Sensitive artifacts remain outside Git with restricted parent-directory access.

### Coverage limits and operational constraints

- Configured SGL upstream still refuses connections (HTTP 502 from gateway); Bedrock still rejects the request with HTTP 403. These were retried on the final image. No local/local_v2 Kimi provider is configured. Deterministic SGL/Bedrock/Kimi regressions pass; live inference for these integrations is **not verified**.
- User clarification after handoff: Bedrock, SGL and Kimi are known pre-existing provider problems. Their repair is outside this upgrade's scope, and their unavailable live checks are not upgrade blockers. This clarification does not change the recorded test outcomes or count unverified checks as passes.
- The 33 core skips are explicit (mostly missing provider integration credentials), not counted as passes. Full core suite is no longer blocked by the old jq.8 schema-test compilation failure.
- Original local deployment has encryption disabled, preserved as requested by existing configuration. Encryption, team exemptions, ownership, cached usage and S3 migration were verified on isolated copies with synthetic keys. Existing legacy-v1 encrypted rows were absent; Go and Portal's intentional legacy-v1 rejection are covered by tests, while owner/boss migration decryption is demonstrated with v2 rows.
- The original database had no alias rules. Alias routing was tested with a temporary post-upgrade fixture, not presented as a historical-alias migration test.
- Full transport lib tests initially hit SQLite database-is-locked; targeted retry and full serial suite pass. UI type checking requires generated route types (normal build produces them).
- Mixed old/new writers on the migrated schema and image-only rollback are unsafe assumptions: OAuth migration drops five columns. Rollback requires restoring the coherent pre-upgrade backup. Pending admin OAuth flows stored only in old configuration must be restarted; pending user OAuth state/verifier and shared/VK credentials were preserved.
- New video_edit_input and guardrail_debug follow the existing cleartext auxiliary-field policy; encryption still covers four conversation fields and removes raw request/response. Legacy content-hidden rows without a v2 policy marker retain upstream hidden-row billing behavior.
- No source/image was pushed or published and no production service was changed. Installed sync-release skill still points to the original checkout; the revised runbook is in the candidate branch for review.

### Final cleanup and reproduction pointers

- Eleven temporary `bifrost-v2-*` rehearsal containers are stopped; their named volumes, bind-mounted fixture data, images and private evidence are retained. Container/image/data inventory: `parked-rehearsal-resources.json`. The normal deploy and observability services remain running. Temporary HTTP mock containers were removed by their harnesses.
- Live virtual-key count is back to the original four; no temporary v2 test keys remain. Original source main still has only its pre-existing .dockerignore edit. Candidate, pristine-upstream control and Portal compatibility worktrees are clean. The temporary copied negative-test file was removed from the pristine control after recording the failure.
- Key reproducible source checks: `PATH="$(go env GOPATH)/bin:$PATH" USE_INFISICAL=0 make test-core` at the candidate root; `GOWORK=off go test -mod=readonly ./bifrost-http/server` under transports. Source regression coverage is committed; local orchestration and private fixtures stay in evidence.
- Local-only rehearsal scripts: `rehearsal.py` (snapshot/restore-container setup/boot), `databasechecks.py` and `comparemigration.py` (schema, historic data, identities and OAuth assertions), `verifyfixtures.py` + `decryptfixtures.mjs` (API ownership and actual Portal crypto), `hybridfixtures.py` + `recalculatehybrid.py` (S3 and billing), `governancehttp.py`, final CLI scripts and `audit-final.py`, and the browser directory's `login.mjs`.
- To revisit saved isolated states, start their PostgreSQL containers and MinIO/mock provider before their Bifrost/Portal containers. Candidate and hybrid use final 2.0.0-jq.1; the upstream-control container currently holds the restored jq.8 rollback baseline. Review script parameters before rerunning mutation/fixture setup; rerunning initial seeding blindly is unnecessary.
- Production promotion is a separate future decision. This task ends with a reviewed local candidate, verified migration and restore procedure, explicit external-provider coverage limits, and preserved recovery data.

### Encrypted payload compatibility confirmation

- Compared the verified implementation commit against exact `v1.6.6-jq.8`: `plugins/logencryption/envelope/` and `plugins/logencryption/marshal.go` have no changes. The encrypted envelope, cryptography and bundle serialization retain their existing format; old ciphertext needs no conversion or re-encryption.
- This confirmation concerns the encrypted payload. Upstream 2.0 changes surrounding log metadata and database schemas, and normal schema migrations still run. Migration decryption coverage uses fixtures written by the old image in PostgreSQL and S3 with the existing owner/boss keys.

### Checked-in HTML report and commit-level evidence audit

- Interactive HTML report: [Bifrost 2.0 rebase and migration report](bifrost-v2-rebase-report.html). It documents all 21 original non-merge commits, 16 replay mappings, five drop/supersession decisions and the two integration fixes. Each entry explains its purpose, conflict/adaptation choices, named tests, evidence sources and coverage limits.
- Machine-readable companion: [sanitized evidence index](bifrost-v2-rebase-evidence.json), containing exact SHAs, terminal test results, migration assertions and SHA-256 hashes for 44 saved artifacts. Private logs, keys, credentials and database dumps remain outside the repository.
- Additional report audit at documentation-only commit 1efee75e4: 303 passing Go cases/subcases across internal storage/plugins, transport, Codex/schema and pristine-upstream additional_tools checks; 73 default-run external DB/vector-generator skips recorded separately. Focused Bifrost UI columns suite: 10/10 pass. Seven build/workspace/version/documentation checks pass; exact upstream workspace script discovers all 18 modules.
- Clarification from source review: legacy-v1 envelopes are intentionally rejected by both Go and Portal. Previous “Go v1 compatibility” wording was inaccurate. The v2 envelope already used by jq.8 remains byte-for-byte unchanged in its implementation, vectors and bundle serializer.
- Publishing-target evidence is limited to review/dry-run and the local build. The retained Make target tags images with the internal suffix but embeds the upstream-only VERSION in the binary, following its pre-existing convention. The tested local image was explicitly built with VERSION=2.0.0-jq.1. No publishing run is claimed.
- Report validation: 97 browser checks pass, including all commit/evidence links, search/filter/reset/expand behavior, desktop and mobile layouts, PDF printing, JavaScript-disabled reading, and zero external requests or JavaScript errors. HTML nesting, JSON parsing and commit/evidence references also pass structural checks.

- Commit metadata extension: the HTML report and evidence index now include Git committer dates, names and emails for all 43 referenced commits. Original and replayed commits are labeled separately; full timestamps retain their stored timezone offsets. Git author metadata is recorded separately to avoid confusing authorship with the committer/date created by rebase.
- Metadata validation: every displayed identity/date matches Git; 102 report checks pass, including date/committer rendering, committer-name search, desktop/mobile layout, offline reading, links and PDF printing.

### Proposed production upgrade plan (2026-09-08)

- Added an [upgrade plan section](bifrost-v2-rebase-report.html#plan) to the HTML report and an `upgrade_plan` entry in its structured evidence index. The plan distinguishes completed local work from production steps that have not been executed.
- Eleven phases cover source/rebase review and release freeze, exact image build/publication, deployment preparation, production-scale migration/restore rehearsal, maintenance/drain, coherent full backups, single-instance schema migration, acceptance checks, controlled traffic reopening, observation and handover, and cleanup/retention. Each phase names proposed owners and an exit gate.
- Grounded deployment details in `llm-gateway-deploy` commit `0d3349a22df4f86be7ba3a4a3740a8ccb228d004`. Its documented internal environment is single-host Compose; generic single/HA deployment is a separate path. The internal execute entrypoint starts eight non-database services together, so the plan requires staged commands or a scoped deployment change before using it for this migration.
- Full rollback coverage must include Bifrost config/log stores, Portal data, matching configuration/app data and object recovery points, plus access to existing keys in their secure custody. The existing `backup-config.sh`/`restore-config.sh` intentionally omit logs and runtime/session data; they do not provide this full recovery set. The HA `gateway-ha-backup` template is a standby/fault hook, not a database backup.
- The plan covers full internal VERSION embedding, registry/platform digests, configuration/consumer compatibility, writer and failover fencing, restored-backup validation, historical/new encrypted data, real OpenAI/OpenRouter and MCP checks, budgets/cached billing, OAuth/SSO, monitoring and post-upgrade backup-policy updates.
- Production owners still need to fill in the actual inventory/artifacts, staged commands, window, outage/data-loss limits, restore timings, rollback deadline, observation thresholds and retention. Local timing and migration counts are evidence, not production guarantees.
- Recovery decisions distinguish aborting before migration, restoring before public traffic and restoring after real writes resume. The last case requires preserving and reconciling post-backup changes; no automatic reverse migration or image-only downgrade is claimed. HA guidance prevents an old standby from becoming a writer on the migrated schema.
- Validation: **111 browser checks pass**, including plan navigation/disclosures, desktop/mobile layout, offline reading, PDF printing, all existing commit metadata/filter behavior, links, and zero external requests or JavaScript errors. HTML nesting and JSON parse checks pass; all 44 historical artifact hashes still match. Historical commit/test evidence is unchanged apart from the report-validation record.
- This addition changes documentation only. No production connection, source/image publication, deployment change or application test rerun was performed for the plan.
