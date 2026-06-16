---
name: sync-release
description: End-to-end internal release of the Bifrost fork. Pulls the latest upstream (GitHub maximhq/bifrost) into main while preserving internal patches, pushes to the internal GitLab via a release branch + MR, builds and pushes the internal Docker image to harbor, and bumps the deploy repo. Invoked with /sync-release [upstream-version] (e.g. /sync-release v1.5.14, or just "跑一遍发布流程" / "sync release"). Encodes the non-obvious traps: protected main branch, glab host-alias mismatch, Makefile conflict resolution, and the version-file mechanism.
allowed-tools: Read, Edit, Bash, AskUserQuestion, TaskCreate, TaskUpdate
---

# sync-release

Run the full internal release of this Bifrost fork in one shot: **pull upstream → push GitLab (branch+MR) → build & push image → bump deploy repo**.

This repo is a **fork**:

- `upstream` = GitHub `git@github.com:maximhq/bifrost.git` — the open-source source of truth.
- `origin` = internal GitLab `ssh://git@gitlab-general.jqdomain.com:8022/jq-public/tech/restech/llm/bifrost.git`.

The internal `main` carries patches that must survive every upstream merge — chiefly the
`push-bifrost` and `helm-index` Makefile targets, `AGENTS.md`, the version files
(`transports/version`, `transports/internal_version`), and encrypted audit-log storage
across `framework/logstore`, `plugins/logencryption`, the transport plugin loader, and
the logs UI. The whole point of this skill is to advance the upstream code **without
dropping those**.

The deploy repo lives at `/data/llm/llm-gateway-deploy` (remote `jarvis-wu/llm-gateway-deploy`).

## Pushing is irreversible — confirm before each outward step

Three steps publish to shared systems and are hard to reverse: the GitLab push, the
`docker push` to harbor, and the deploy-repo push. **Confirm the target version with the
user up front** (`AskUserQuestion`), then proceed through the rest without re-prompting at
every step unless something looks wrong. Keep a `backup/main-pre-<version>-merge` branch so
the merge can be undone locally.

---

## Worktree and major-version adaptations

The paths, remote URLs and host aliases below describe an older workstation.
Discover the active repository with `git rev-parse --show-toplevel`, inspect its
remotes and the local Compose project, and locate the current deploy checkout
before using those examples. Do not rewrite remotes to match an example.

When the user explicitly requests a rebase or a local rehearsal, follow that scope.
Use an isolated feature worktree and a backup ref; audit each internal patch and
drop one only with upstream equivalence evidence. A local validation request does
not authorize the publishing steps below. For 2.0 migration and billing/storage
constraints, read `docs/internal/v2-upgrade.md`. Internal version suffixes restart
at jq.1 when adopting a new upstream release.

## Step 0 — Establish state (always do this first)

```bash
cd /data/llm/bifrost
git remote -v                                   # confirm upstream=GitHub, origin=GitLab
git fetch upstream --tags
git fetch origin
git status -sb
cat transports/version transports/internal_version
git tag -l 'transports/*' | sort -V | tail -8   # find the latest upstream release tag
```

- The image tag is computed by the Makefile as `v$(transports/version)-$(transports/internal_version)`
  → e.g. `v1.5.14-jq.1`. **`transports/version` is updated automatically by the upstream merge**
  (it comes from the upstream tag); you only ever hand-edit `internal_version` (e.g. `jq.1`).
- Confirm the target with the user: which upstream version to merge, and the resulting image
  tag. Default target = the latest `transports/vX.Y.Z` tag. Prefer **merging the tag**, not
  `upstream/main` HEAD — the tag is the frozen release point and matches the image name exactly.

## Step 1 — Merge upstream into main, preserving internal patches

```bash
git branch -f backup/main-pre-<version>-merge main      # safety net
git merge --no-commit --no-ff transports/<version>      # e.g. transports/v1.5.14
```

`Makefile` is a common conflict. Upstream adds its own vars/targets; we keep ours.
Resolve by **keeping both sides** — take the upstream additions (e.g. a new `COMPAT ?=`
var) AND retain the internal block:

```
IMAGE_REGISTRY ?= harbor.jqdomain.com/hpc-llm-tools
BIFROST_VERSION_FILE := transports/version
BIFROST_INTERNAL_VERSION_FILE := transports/internal_version
BIFROST_VERSION := $(shell sed -n '1p' $(BIFROST_VERSION_FILE) 2>/dev/null)
BIFROST_INTERNAL_VERSION := $(shell sed -n '1p' $(BIFROST_INTERNAL_VERSION_FILE) 2>/dev/null)
BIFROST_IMAGE_REPOSITORY ?= $(IMAGE_REGISTRY)/bifrost
BIFROST_PUSH_VERSION := v$(BIFROST_VERSION)-$(BIFROST_INTERNAL_VERSION)
BIFROST_PUSH_IMAGE := $(BIFROST_IMAGE_REPOSITORY):$(BIFROST_PUSH_VERSION)
```

After resolving, **verify the internal targets and .PHONY survived** (the upstream `.PHONY`
line can silently drop them):

```bash
grep -n "^<<<<<<<\|^>>>>>>>\|^=======" Makefile     # must be empty
grep -n "^push-bifrost:\|^helm-index:" Makefile     # both must exist
grep "^.PHONY:" Makefile | grep -o "push-bifrost\|helm-index"   # both must appear
git add Makefile && git commit --no-edit
cat transports/version transports/internal_version  # version bumped, internal unchanged
```

Encrypted audit logging also modifies high-churn upstream files. Never resolve
these with blanket `--ours`/`--theirs`:

```text
framework/logstore/hybrid.go
framework/logstore/payload.go
framework/logstore/tables.go
transports/bifrost-http/server/plugins.go
ui/app/workspace/logs/views/columns.tsx
ui/lib/utils/logEncryption.ts
```

After resolving an upstream merge, verify the generic object-only contract,
the decorator registration, and both envelope generations still exist:

```bash
rg "PayloadStorageObjectOnly|putObjectOnlyPayload" framework/logstore
rg "WrapLogStoreFromEnv" transports/bifrost-http/server/plugins.go
rg "__jq_log_encryption_v1|__jq_log_encryption_v2" plugins/logencryption ui/lib/utils/logEncryption.ts
```

Then run the internal compatibility gates. The first command deliberately
disables the workspace so stale module requirements cannot be hidden:

```bash
cd transports && GOWORK=off go test -mod=readonly ./bifrost-http/server
cd ../framework && go test ./logstore
cd ../plugins/logencryption && go test -mod=readonly ./...
```

Sanity-check the merge result vs the backup — only `transports/version` should differ in the
tracked internal files:

```bash
git diff backup/main-pre-<version>-merge..main -- transports/version transports/internal_version AGENTS.md
```

### Guard against silent file loss

`git diff main origin/main` will report hundreds of "deleted" files — this is **history-shape
noise**, not real loss (internal `main` was rebuilt at some point; see `backup/main-before-reset`).
Verify what is *actually* missing from the merged tree, then confirm those are files **upstream
itself deleted** between the old and new version (not internal-only files):

```bash
# files present on origin/main but absent from the merged main:
while IFS= read -r f; do git cat-file -e main:"$f" 2>/dev/null || echo "MISSING: $f"; done \
  < <(git ls-tree -r --name-only origin/main)
# for each MISSING file, confirm upstream also lacks it (= safe to drop):
git cat-file -e transports/<version>:"$f" && echo "STILL IN UPSTREAM — investigate"
```

If every missing file is also gone from the upstream tag, the drop is correct. If any missing
file is internal-only, stop and reconcile before pushing.

## Step 2 — Push to GitLab via release branch + MR

**`origin/main` is a protected branch — force-push is rejected, and the merge is not a
fast-forward.** Do **not** try to push `main` directly. Push the merged commit to a release
branch and open an MR:

```bash
git push origin main:refs/heads/release/<version>-<internal>   # e.g. release/v1.5.14-jq.1
```

### Creating the MR with glab — the host-alias trap

`glab` is authenticated to host **`gitlab-public.jqdomain.com`**, but the git remote uses
**`gitlab-general.jqdomain.com`**. These are the **same GitLab instance** (both resolve to
`10.8.105.164`), but glab refuses to act because the remote host name doesn't match its
configured host. Work around it with a temporary remote on the public alias, then remove it:

```bash
git remote add glab-public ssh://git@gitlab-public.jqdomain.com:8022/jq-public/tech/restech/llm/bifrost.git
glab mr create \
  --source-branch release/<version>-<internal> --target-branch main \
  --title "release: merge upstream <version> (image v<version>-<internal>)" \
  --description "合并上游 tag transports/<version> 到 main，保留内部补丁（push-bifrost / helm-index target、internal_version）。镜像 v<version>-<internal> 已推送 harbor。冲突仅 Makefile，已保留 COMPAT（上游）+ 内部镜像变量。" \
  --remove-source-branch=false --yes
git remote remove glab-public
```

The **merge of the MR is manual** (protected branch, may need approval). Report the MR URL and
let the user click merge. The image build and deploy bump below do **not** depend on the MR
being merged — they operate on the code content, which is already on the branch.

## Step 3 — Build & push the internal image

The Makefile reads the version files and pushes to harbor. Requires docker + harbor login.

```bash
make -n push-bifrost | grep -o 'harbor[^ ]*bifrost:[^ ]*'   # confirm the tag first
docker login harbor.jqdomain.com                            # if not already logged in
make push-bifrost
```

`make push-bifrost` runs `docker build -f transports/Dockerfile.local` then `docker push`.
It is **slow** (UI `npm ci` + Go module download + build, several minutes) — run it in the
**background** (`run_in_background: true`).

**Background-monitoring trap:** the wrapper shell around `sleep`/`tail` polling may be killed
with **exit code 143 (SIGTERM)** by the environment, even though the underlying `docker build`
keeps running in the daemon. Do **not** conclude the build failed on a 143. Verify the real
outcome directly:

```bash
ps aux | grep -E "docker build|make push" | grep -v grep   # still running?
docker images | grep "bifrost.*v<version>-<internal>"      # image produced?
tail -n 5 <output-file>                                     # look for "Pushed internal Bifrost image"
```

Only treat the step as done when the image appears in `docker images` and the log shows the
`Pushed internal Bifrost image: ...` line.

## Step 4 — Bump the deploy repo

```bash
cd /data/llm/llm-gateway-deploy
git fetch origin && git status -sb
grep -rn "bifrost_image_version" group_vars/   # currently only group_vars/prod.yml line ~6
```

Edit `group_vars/prod.yml`: `bifrost_image_version: "v<old>"` → `"v<version>-<internal>"`.
The `image_registry` there (`harbor.jqdomain.com/hpc-llm-tools`) must match the Makefile's,
so the deployed image path lines up with what Step 3 pushed.

This repo's `main` is **not** protected — commit and push directly. Do this **after** Step 3
confirms the image exists in harbor, so the deploy never points at a missing tag.

```bash
git add group_vars/prod.yml
git commit -m "chore: bump bifrost image to v<version>-<internal>"
git push origin main
```

---

## Final report

Report concisely:

1. **GitLab** — release branch pushed, **MR URL** (and that merge is manual / pending approval).
2. **Image** — full tag pushed to harbor + digest.
3. **Deploy** — `prod.yml` bumped old→new, pushed (commit range).
4. **Backup** — `backup/main-pre-<version>-merge` retained for rollback.

## Quick reference — the traps this skill exists to remember

- `transports/version` is set **by the merge**; only `internal_version` is hand-edited.
- Merge **the tag**, not `upstream/main` HEAD.
- `Makefile` and encrypted-audit hot spots may conflict — keep upstream behavior
  and the internal storage/envelope contracts; never take one side wholesale.
- Re-run the no-workspace transport build, logstore tests, log-encryption tests,
  and cross-language Portal audit regressions after every upstream merge.
- `git diff main origin/main` mass "deletions" are history noise — verify real loss against the
  merged tree and confirm drops are upstream-driven.
- `origin/main` is **protected** → release branch + MR, never force-push.
- glab host alias: authed on `gitlab-public.jqdomain.com`, remote on `gitlab-general.jqdomain.com`
  (same IP) → temporary `glab-public` remote, then remove.
- `docker build` background polling may exit **143** while the build keeps running — verify via
  `docker images`, not the wrapper's exit code.
- deploy repo `main` is **not** protected — push directly, but only after the image is in harbor.
