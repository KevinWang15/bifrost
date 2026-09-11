"""End-to-end assertions against the isolated compose stack (no paid APIs)."""
import json
import os
import time
import urllib.request

GATEWAY = os.environ.get("BIFROST_URL", "http://127.0.0.1:28080")
FAKE = os.environ.get("FAKE_LLM_URL", "http://127.0.0.1:29090")

def call(url, body=None, headers=None):
    req = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(),
        headers={"Content-Type": "application/json", **(headers or {})})
    with urllib.request.urlopen(req, timeout=20) as r:
        raw = r.read().decode()
        return raw if "text/event-stream" in r.headers.get("Content-Type", "") else json.loads(raw)

def request(session, model="balanced", stream=False, header="session-id"):
    return call(GATEWAY + "/v1/chat/completions", {"model": model, "stream": stream,
        "messages": [{"role": "user", "content": "repeatable cache prefix"}], "max_tokens": 8}, {header: session})

def selected(result):
    return result["choices"][0]["message"]["content"]

def set_status(name, status):
    call(FAKE + "/control", {"route": name, "status": status})

def wait_for(predicate, timeout=20):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if predicate():
            return
        time.sleep(.2)
    raise AssertionError("condition did not become true before deadline")

for name in ("a", "b"):
    set_status(name, 200)

for header in ("session-id", "session_id", "x-claude-code-session-id", "x-bf-session-id"):
    session = header + str(time.time_ns())
    first = selected(request(session, header=header))
    for _ in range(12):
        result = request(session, header=header)
        assert selected(result) == first, ("session moved", header, first, result)
        assert result["usage"]["prompt_tokens_details"]["cached_tokens"] == 100, result
    print("PASS session binding and simulated prefix-cache reuse:", header)

session = "outage-" + str(time.time_ns())
primary = selected(request(session))
backup = "b" if primary == "a" else "a"
baseline = sum(r["status"] == 503 and "repeatable cache prefix" in json.dumps(r["messages"]) for r in call(FAKE + "/state")[primary]["requests"])
set_status(primary, 503)
assert selected(request(session)) == backup
# Inspect actual fake-upstream records: probes carry a different, fixed prompt.
for _ in range(8):
    assert selected(request(session)) == backup
for _ in range(8):
    assert selected(request("new-" + str(time.time_ns()))) == backup
records = call(FAKE + "/state")[primary]["requests"]
failed_business = [r for r in records if r["status"] == 503 and "repeatable cache prefix" in json.dumps(r["messages"])]
assert len(failed_business) == baseline + 1, failed_business
print("PASS outage is remembered across requests and sessions")

recovery_started = time.time()
set_status(primary, 200)
wait_for(lambda: any(r["status"] == 200 and r["at"] >= recovery_started and "Bifrost health probe" in json.dumps(r["messages"])
                    for r in call(FAKE + "/state")[primary]["requests"]))
assert selected(request(session)) == backup, "recovery moved an established session"
seen = {selected(request("recovered-" + str(time.time_ns()))) for _ in range(40)}
assert seen == {"a", "b"}, seen
print("PASS background probe restores traffic before cooldown, old session stays on backup")

session = "stream-" + str(time.time_ns())
first = selected(request(session))
raw = request(session, stream=True)
chunks = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith("data: ") and line != "data: [DONE]"]
assert any(c.get("choices", [{}])[0].get("delta", {}).get("content") == first for c in chunks), raw
assert "[DONE]" in raw
print("PASS streaming requests share the session target")

# Startup failure during an SSE request must update the binding after the
# successful fallback stream, not just for unary requests.
stream_session = "stream-fallback-" + str(time.time_ns())
stream_primary = selected(request(stream_session))
stream_backup = "b" if stream_primary == "a" else "a"
set_status(stream_primary, 503)
raw = request(stream_session, stream=True)
chunks = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith("data: ") and line != "data: [DONE]"]
assert any(c.get("choices", [{}])[0].get("delta", {}).get("content") == stream_backup for c in chunks), raw
assert selected(request(stream_session)) == stream_backup, "successful stream fallback was not retained"
recovery_started = time.time()
set_status(stream_primary, 200)
wait_for(lambda: any(r["status"] == 200 and r["at"] >= recovery_started and "Bifrost health probe" in json.dumps(r["messages"])
                    for r in call(FAKE + "/state")[stream_primary]["requests"]))
print("PASS SSE startup fallback updates the session binding")

# A key/request-level 4xx must not quarantine an entire provider/model route.
for status in (400, 401, 429):
    set_status("a", status)
    for _ in range(2):
        try:
            request("4xx-" + str(time.time_ns()), model="a/gpt-test")
            raise AssertionError("expected upstream error")
        except urllib.error.HTTPError as err:
            payload = json.loads(err.read())
            assert payload.get("error", {}).get("code") != "route_unavailable", payload
    set_status("a", 200)
print("PASS request/auth/rate-limit errors do not quarantine a model route")

# All-down behavior must be bounded: return an error and make no additional
# business calls while the routes remain open (background probes are separate).
for name in ("a", "b"):
    set_status(name, 503)
try:
    request("all-down-" + str(time.time_ns()))
    raise AssertionError("all-down request unexpectedly succeeded")
except urllib.error.HTTPError:
    pass
before = call(FAKE + "/state")
for _ in range(3):
    try:
        request("all-down-" + str(time.time_ns()))
        raise AssertionError("all-down request unexpectedly succeeded")
    except urllib.error.HTTPError as err:
        assert err.code == 503, err.code
        assert json.loads(err.read())["error"]["code"] == "route_unavailable"
after = call(FAKE + "/state")
for name in ("a", "b"):
    business = lambda s: sum("repeatable cache prefix" in json.dumps(r["messages"]) for r in s[name]["requests"])
    assert business(before) == business(after), "open route received another business request"
recovery_started = time.time()
for name in ("a", "b"):
    set_status(name, 200)
wait_for(lambda: all(any(r["status"] == 200 and r["at"] >= recovery_started and "Bifrost health probe" in json.dumps(r["messages"])
                        for r in call(FAKE + "/state")[name]["requests"]) for name in ("a", "b")))
print("PASS all-down fail-fast and automatic recovery")

# Exercise the actual Responses wire format (including Responses SSE events).
def responses(session, stream=False):
    return call(GATEWAY + "/v1/responses", {"model": "balanced", "input": [{"role": "user", "content": "responses prefix"}],
        "max_output_tokens": 16, "stream": stream}, {"session-id": session})

def responses_target(response):
    return response["output"][0]["content"][0]["text"]

rsession = "responses-" + str(time.time_ns())
rprimary = responses_target(responses(rsession))
for _ in range(8):
    assert responses_target(responses(rsession)) == rprimary
raw = responses(rsession, True)
events = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith("data: ") and line != "data: [DONE]"]
completed = [e for e in events if e.get("type") == "response.completed"]
assert completed and responses_target(completed[-1]["response"]) == rprimary, raw
print("PASS Responses API session binding and streaming wire format")

set_status(rprimary, 503)
rbackup = "b" if rprimary == "a" else "a"
assert responses_target(responses(rsession)) == rbackup
for _ in range(4):
    assert responses_target(responses(rsession)) == rbackup
recovery_started = time.time()
set_status(rprimary, 200)
wait_for(lambda: any(r["status"] == 200 and r["at"] >= recovery_started and "Bifrost health probe" in json.dumps(r["messages"])
                    for r in call(FAKE + "/state")[rprimary]["requests"]))
assert responses_target(responses(rsession)) == rbackup
print("PASS Responses API cross-model fallback, probe recovery and retained destination")

# A fallback binding must expire through the common session-TTL header, while
# recovery alone must preserve an otherwise active session.
ttl_session = "ttl-" + str(time.time_ns())
ttl_body = {"model": "a/gpt-test", "fallbacks": ["b/glm-test"],
    "messages": [{"role": "user", "content": "session ttl regression"}], "max_tokens": 8}
set_status("a", 503)
assert selected(call(GATEWAY + "/v1/chat/completions", ttl_body,
    {"session-id": ttl_session, "x-bf-session-ttl": "1s"})) == "b"
recovery_started = time.time()
set_status("a", 200)
wait_for(lambda: any(r["status"] == 200 and r["at"] >= recovery_started and "Bifrost health probe" in json.dumps(r["messages"])
    for r in call(FAKE + "/state")["a"]["requests"]))
time.sleep(1.1)
assert selected(call(GATEWAY + "/v1/chat/completions", ttl_body, {"session-id": ttl_session})) == "a"
print("PASS common session TTL expires fallback affinity through the KV store")
