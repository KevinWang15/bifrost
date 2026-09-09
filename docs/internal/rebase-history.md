# Bifrost v2 history consolidation — 2026-09-09

The feature branch now contains 16 replayed internal changes with their fixups folded in, one separate OpenRouter upstream-regression fix, and one dedicated migration/testing/evidence/plan documentation commit.

Pre-rewrite tip: `3235dbfb0a16fa1845051e13079e5696654dccf1`. Recovery ref: `backup/v2-before-fixup-squash-20260909`.
Rewritten implementation: `786b4f3128bf653f31948a74a7c83df34c3f872f`. Upstream base: `e4a30d6041c0446603aea615bc5da340dac001b1`.

The application, tests, dependencies, build configuration, and release runbook are byte-identical to the pre-rewrite tip. The only final-tree differences are documentation notes and this commit mapping. Before adding those notes, the reconstructed tree matched the old complete Git tree exactly.

Historical SHAs, image identities, test results, timestamps, and evidence hashes in the report, tracker, and evidence JSON are retained as records of the code actually tested. They identify the pre-rewrite history, available through the recovery ref; use the mapping below for the current branch. The rewrite does not claim that the old image embeds a new commit SHA or that live/migration tests were rerun.

| # | Change | Original v1 | Previous v2 | Current v2 |
|---|---|---|---|---|
| 1 | build: add push-bifrost target for internal image publishing | `231f8353f91115bd6ba72a8a0f9b0870240d11ac` | `378b4f61c035276ffa5c40f7f7767b23c77c1295` | `3b66769bbdbcffb32b5001048cd83e10d3323dae` |
| 2 | chore: add sync-release skill for upstream sync + internal release | `2a156ec70cba9303015b40a61e33b28ee23bcd72` | `c706e7fceb06d50ecc8f73405a6e48b4d416ff68` | `f95752ee2902e403383f4bbf0a3ede7274ff782c` |
| 3 | feat(logencryption): encrypt audit log content at rest | `3f847ff17e48ffb9274fa5af95f1055b6f9fa357` | `a1aaf9a4aff0f7e7fd221bda61ad4d23d23e4121` | `8f5113e0e4efdc5a1525df0f482250061a2bbc57` |
| 4 | perf(logs): avoid rendering encrypted audit ciphertext | `3eacb750997d29c1ed888d72d358fae300d35b55` | `6bbcff51b05fa3be7571ece31cdf5aa9cdac3ee5` | `4bad871a114ba007893156e5b31a51e6eea162be` |
| 5 | feat(logencryption): keep encrypted payloads in object storage | `f0cf832fec249c6e831261013ac385a33fcbf0ce` | `c9cc9e1cce9982d6f9cd62c0074e485a8b14a8d5` | `a98e3bae53e3baa7f228d3db7ffc5cfe4e7d6c22` |
| 6 | docs(bifrost): document internal fork workflow | `7d7d576b607d48e4e5afc4f097fb813ff43fdcb2` | `bd41afcee1d2429a7ed74c5c7f03a704afeff268` | `6cf7cab0c350284d949a8e2d566bb07e07ebaca1` |
| 7 | fix(openai): send explicit stream flag for responses | `4db79729950ea64ec10ebbc2dfc48d2c24487512` | `dfce6f550cbcaaccc91a65f56749ad9918d83b0a` | `97ded40c1874992c4729b6937597f7fd4cf31384` |
| 8 | fix(modelcapabilityvalidator): validate image input capabilities | `07cd82e8849cde9557418348d997ccdadb138fce` | `1ad38099d52a2e1f802a596d4036d302cdba6a9b` | `2a50bea6cdfae55fc9c4e6d66070ca614970523e` |
| 9 | fix(sgl): preserve tools with structured responses | `9c7180747100856fd93e2ee842e1340bf67af6d0` | `44ea5df9e13a7239550ecda8266598f61405f65f` | `6d0f00a007359a292558335b6e696f3bed2b963f` |
| 10 | fix(openrouter): forward Responses cache control | `226efe6c32380eb28d0fba560ed94bde5d4fb49d` | `133a4c49a643622da4be3b464632aaa8b38c33fc` | `cc9edc84d1180197992adcae46fbacb138fd46b0` |
| 11 | fix(openai): fall back Kimi-K3 responses to chat | `db3f527b735a65d5e08fe9eecd8b7791b5d885f2` | `c644717ecd89d947c6fc16b6127742a513c81c80` | `e77be0443d44f9c1c5901b9ca3b98d9fbc338313` |
| 12 | fix(compat): bridge namespace tools for non-OpenAI providers | `5e105d3c009f7e9b32ccf503a3039b9e94fbf372` | `48e89a80fdc8f9f27ae937cf715c352978aa6783` | `94524a6e41a40b6e575c53e765d4d299cdd51e98` |
| 13 | fix(mcp): recognize current Codex clients | `e70f4493a4c37076ef4fb2b0f8d46d6b582c7e02` | `5c2c6eb5df35ac6c6813717daa3baafb08482a6c` | `48c5a2e2212abac5b696531c20026d97dcb7e0db` |
| 14 | fix(compat): preserve namespace tool identity and native Codex detection | `f6ffc1dfbf4aa0db9d2940cf6262e4970aaadade` | `61accf4db54c87b9cbb03e3a727de4f94dc77206` | `87b9278d333a90d9239fe0636bf64d6f7a829240` |
| 15 | fix(compat): make namespace restoration idempotent | `5c2552a3d413f97a6bc34b6e1d4fa781da646d66` | `846aa3439f1e382ad7b1c381a3986384ae8b784c` | `ad38358af37f97cb4d860a313032a892af1f918d` |
| 16 | fix: terminate incomplete Bedrock streams | `3d0701bb329a2ae76bdeb98f9f7a03f5edbb6706` | `73bba745b97d1aa8308c21d78c6cad824a8dcbfb` | `fbd97d92c808326beaa1eae3b99f3f8dd09aa578` |
| 17 | fix(openrouter): preserve encrypted reasoning IDs through Anthropic replay | `new upstream regression fix` | `8654f269a163c4f7795a85d0ca89f0804d4a6c78` | `786b4f3128bf653f31948a74a7c83df34c3f872f` |

Fixup allocation:

- `168b77d40` billing logic and regression tests are folded into the object-storage commit. Its encryption and validator module updates are folded into the respective plugin commits.
- Final `.dockerignore` exclusions are folded into the internal build commit; final release-runbook guidance is folded into the release-skill commit.
- Historical internal-version increments are removed. Every rewritten internal commit uses `jq.1`.
- Migration notes from `168b77d40` and `8654f269a`, plus `1efee75e4`, `7364a3da3`, `1ed292050`, and `3235dbfb0`, are consolidated in the final documentation commit. Functional regression tests stay with their owning code changes.

Review commands:

```bash
git log --reverse --oneline e4a30d6041c0446603aea615bc5da340dac001b1..HEAD
git diff backup/v2-before-fixup-squash-20260909 HEAD -- . ':(exclude)docs/'
git diff --stat backup/v2-before-fixup-squash-20260909 HEAD
git diff --check
```

The second command must print nothing. The third must list documentation paths only. The history must contain 18 commits above upstream and the last commit must modify documentation only. No publishing or deployment is part of this rewrite.
