"""Verify real HTTP routing/failover without contacting paid providers."""
import concurrent.futures
import json
import time
import urllib.error
import urllib.request
import uuid

GATEWAY = "http://gateway:8080"
UPSTREAM = "http://providers:9000"


def http(base, path, body=None, headers=None, method=None):
    req = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(), headers={"Content-Type": "application/json", **(headers or {})}, method=method)
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            return response.status, response.read().decode()
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode()


def stats():
    return json.loads(http(UPSTREAM, "/stats")[1])


def control(**changes):
    assert http(UPSTREAM, "/control", changes)[0] == 200


def reasoning(token):
    return {"id": "rs_" + uuid.uuid4().hex, "type": "reasoning", "summary": [{"type": "summary_text", "text": "plan"}], "encrypted_content": token}


def request(history, provider="provider-b", stream=False, model="gpt-5.6-sol", **extra):
    return {"model": provider + "/" + model, "input": history + [{"role": "user", "content": "continue"}], "stream": stream, **extra}


def success(body, path="/v1/responses", headers=None):
    status, text = http(GATEWAY, path, body, headers)
    assert status == 200, (status, text)
    assert "ok" in text and "unable to decode" not in text, text
    if body.get("stream"):
        assert "response.completed" in text or "message_stop" in text, text
    return text


def case(name, fn):
    fn()
    print("PASS " + name, flush=True)


for _ in range(60):
    try:
        if http(GATEWAY, "/health")[0] == 200:
            break
    except (OSError, urllib.error.URLError):
        pass
    time.sleep(1)
else:
    raise AssertionError("gateway did not become healthy")


def repeated(stream):
    history = [{"role": "user", "content": "start"}, reasoning("A::" + uuid.uuid4().hex)]
    start = len(stats())
    text = success(request(history, stream=stream))
    assert len(stats()) - start == 2
    if not stream:
        native = json.loads(text)["output"][0]
    else:
        native = reasoning("B::" + uuid.uuid4().hex)
    history.append(native)
    for _ in range(3):
        before = len(stats())
        success(request(history, stream=stream))
        sent = stats()[before:]
        assert len(sent) == 1, sent
        assert native["encrypted_content"] in json.dumps(sent[0]["body"]), sent
        assert "A::" not in json.dumps(sent[0]["body"]), sent


case("unary history: 2,1,1,1 calls and B reasoning retained", lambda: repeated(False))
case("streaming history: 2,1,1,1 calls and B reasoning retained", lambda: repeated(True))


def failover(stream):
    control(a_down=False)
    history = [{"role": "user", "content": "start"}]
    source = json.loads(success(request(history, provider="provider-a")))
    history += source["output"]
    control(a_down=True)
    before = len(stats())
    success(request(history, provider="provider-a", stream=stream, fallbacks=["provider-b/gpt-5.6-sol"]))
    assert [r["target"] for r in stats()[before:]] == ["A", "B", "B"]
    before = len(stats())
    success(request(history, provider="provider-a", stream=stream, fallbacks=["provider-b/gpt-5.6-sol"]))
    assert [r["target"] for r in stats()[before:]] == ["A", "B"]
    control(a_down=False)
    before = len(stats())
    success(request(history, provider="provider-a"))
    sent = stats()[before:]
    assert len(sent) == 1 and "A::" in json.dumps(sent[0]["body"]), sent


case("unary A down -> B fallback, then A recovery", lambda: failover(False))
case("streaming A down -> B fallback, then A recovery", lambda: failover(True))


def routing():
    history = [reasoning("A::" + uuid.uuid4().hex)]
    for expected in (2, 1):
        before = len(stats())
        success(request(history, provider="provider-a"), headers={"x-test-route": "b"})
        sent = stats()[before:]
        assert len(sent) == expected and all(r["target"] == "B" for r in sent), sent


case("CEL routing switch keeps cached decision", routing)


def model_isolation():
    history = [reasoning("A::" + uuid.uuid4().hex)]
    success(request(history))
    before = len(stats())
    success(request(history, model="gpt-5.5"))
    assert len(stats()) - before == 2, "decision leaked to another model"


case("model isolation", model_isolation)


def failed_recovery():
    control(always_reject=True)
    history = [reasoning("A::" + uuid.uuid4().hex)]
    for _ in range(2):
        before = len(stats())
        status, text = http(GATEWAY, "/v1/responses", request(history))
        assert status == 400, (status, text)
        assert len(stats()) - before == 2, "failed retry was cached"
    control(always_reject=False)


case("failed recovery does not train cache", failed_recovery)


def late_stream_failure():
    control(stream_fail=True)
    history = [reasoning("A::" + uuid.uuid4().hex)]
    for _ in range(2):
        before = len(stats())
        _, text = http(GATEWAY, "/v1/responses", request(history, stream=True))
        assert "failed" in text, text
        assert len(stats()) - before == 2, "failed stream trained cache"
    control(stream_fail=False)


case("late stream failure does not train cache", late_stream_failure)


def anthropic(stream):
    token = "A::" + uuid.uuid4().hex
    history = [{"role": "user", "content": "start"}, {"role": "assistant", "content": [{"type": "thinking", "thinking": "plan", "signature": token}, {"type": "text", "text": "answer"}]}]
    for expected in (2, 1):
        before = len(stats())
        success({"model": "claude-haiku-4-5", "max_tokens": 128, "stream": stream, "messages": history + [{"role": "user", "content": "continue"}]}, path="/anthropic/v1/messages")
        assert len(stats()) - before == expected
        if expected == 2:
            history.append({"role": "user", "content": "continue"})
            history.append({"role": "assistant", "content": [{"type": "thinking", "thinking": "native", "signature": "B::native"}, {"type": "text", "text": "ok"}]})
        else:
            assert "B::native" in json.dumps(stats()[-1]["body"])


case("Anthropic Messages signed thinking", lambda: anthropic(False))
case("Anthropic Messages signed thinking stream", lambda: anthropic(True))


def concurrency():
    history = [reasoning("A::" + uuid.uuid4().hex)]
    success(request(history))
    before = len(stats())
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        list(pool.map(lambda _: success(request(history)), range(16)))
    assert len(stats()) - before == 16, "cached concurrent turns retried"


case("16 concurrent cached requests", concurrency)


def metadata_update(stream):
    history = [reasoning("A::" + uuid.uuid4().hex)]
    success(request(history, stream=stream))
    headers = {"X-Bifrost-Setup-Token": "local-reasoning-cache-setup"}
    status, text = http(GATEWAY, "/api/providers/provider-b/keys", headers=headers)
    assert status == 200, (status, text)
    key = json.loads(text)["keys"][0]
    updated = {"name": key["name"], "value": "local-B", "models": ["*"], "weight": 1, "enabled": True, "use_for_batch_api": not key.get("use_for_batch_api", False)}
    status, text = http(GATEWAY, "/api/providers/provider-b/keys/" + key["id"], updated, headers=headers, method="PUT")
    assert status == 200, (status, text)
    result = json.loads(text)
    assert result["id"] == key["id"] and result["use_for_batch_api"] == updated["use_for_batch_api"], result
    before = len(stats())
    success(request(history, stream=stream))
    sent = stats()[before:]
    assert len(sent) == 1, ("administrative update invalidated cached recovery", sent)


case("unary cached recovery survives key metadata update", lambda: metadata_update(False))
case("streaming cached recovery survives key metadata update", lambda: metadata_update(True))
print("All container scenarios passed.", flush=True)
