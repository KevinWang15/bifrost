# Remember rejected reasoning: local integration suite

Run from the repository root:

```bash
./tests/reasoning-cache/run.sh
# Include the ten fixture-only Newman requests:
./tests/reasoning-cache/run.sh --harness
```

This builds the HTTP gateway from local modules in Go 1.27 containers, then runs it
alongside fake providers. No real API credentials or paid providers are used. The test
services use a separate Compose project and no host ports; the script removes
its containers and network on exit. Build output and caches remain reusable.
An ignored UI placeholder is created only when no UI build is present.

`verify.py` asserts upstream call counts, rather than timing, for unary and SSE
conversation replay, CEL routing, A-to-B failover, switching back to A, model
isolation, unsuccessful recovery, late stream errors, Anthropic Messages,
concurrent follow-ups, and key metadata updates. It also checks that new B reasoning
survives cached rewrites. On unchanged client history, B receives two calls on the first turn
and one call on each following turn.

The provider-harness collection also contains the fixture-only folder
`144. Remember rejected reasoning across conversation turns`. Its ten requests
are marked `[PREVIEW]` to exclude them from ordinary live-provider sweeps. Run
that folder with Newman on this Compose network, `include_preview=1`,
`baseUrl=http://gateway:8080`, and
`reasoningCacheProviderURL=http://providers:9000`. Management requests also need
`setupToken=local-reasoning-cache-setup`, which `run.sh --harness` supplies.

The implementation keeps at most 16,384 rejection fingerprints for 24 hours in
each Bifrost instance. Decisions include the provider, model, endpoint,
provider-specific authentication and routing settings, credential, caller identity,
and forwarded headers. Administrative key metadata and alias descriptions do not
invalidate a learned decision. All forwarded headers remain in the identity because
custom upstreams can use arbitrary headers to select accounts or routes. Cache
contents are SHA-256 hashes, not ciphertext or credentials. An instance restart, expiry,
or eviction can require another initial recovery; replicas learn independently.

Learning requires a successful stripped retry. Streams must reach a successful
terminal event. Item-ID and prompt-prefix mismatch errors are not remembered
because a client can repair those without replacing the opaque token. Cached
rewrites detach request structs, retain new/unknown reasoning, and preserve the
original history for different keys and fallback providers.

A successful bulk strip proves that removing the set fixed the request, not that
every member was incompatible. This first implementation remembers the removed
payloads from that successful recovery; if the initial rejected history mixed
valid and invalid tokens, both can be remembered. Newly generated B tokens are
unaffected. Precise attribution using upstream error paths is future work.
Large-payload streaming passthrough retains the existing behavior: it is not
rewritten or cached.
