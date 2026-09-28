"""Run inside the Compose probe container. Uses only the local mock backend."""
import json
import pathlib
import time
import urllib.error
import urllib.request
import uuid

BASE = "http://bifrost:8080"
results = []


def request(url, data=None, headers=None):
    req = urllib.request.Request(url, data=None if data is None else json.dumps(data).encode(),
                                 headers={"Content-Type": "application/json", **(headers or {})})
    start = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=190) as response:
            return response.status, response.read().decode(), dict(response.headers), time.monotonic() - start
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode(), dict(error.headers), time.monotonic() - start


def check(name, condition, detail):
    results.append({"name": name, "passed": bool(condition), "detail": detail})
    print(json.dumps(results[-1]), flush=True)


def chat(messages, session=None, **options):
    headers = {"x-switchyard-session-id": session or str(uuid.uuid4())}
    return request(BASE + "/v1/chat/completions",
                   {"model": "switchyard/test", "messages": messages, "max_tokens": 128, **options}, headers)


def user(text):
    return {"role": "user", "content": text}


def judge_count():
    return sum(c["model"] == "mock-judge" for c in json.loads(request("http://mock:9000/stats")[1]))


for attempt in range(90):
    try:
        if request(BASE + "/health")[0] == 200 and request("http://switchyard:4000/health")[0] == 200:
            break
    except (OSError, urllib.error.URLError):
        pass
    time.sleep(1)
else:
    raise SystemExit("Stack did not become healthy")

for label, prompt, expected in [("simple", "SIMPLE", "mock-small"),
                                 ("complex", "COMPLEX", "mock-big"),
                                 ("invalid verdict", "INVALID_VERDICT", "mock-big")]:
    status, raw, headers, elapsed = chat([user(prompt)])
    body = json.loads(raw)
    check(label, status == 200 and body.get("model") == expected,
          {"status": status, "model": body.get("model"), "body": body if status != 200 else None})

status, raw, headers, elapsed = chat([user("SIMPLE")], stream=True)
chunks = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith("data: ") and line[6:] != "[DONE]"]
text = "".join(c.get("choices", [{}])[0].get("delta", {}).get("content", "") or "" for c in chunks if c.get("choices"))
check("chat streaming", status == 200 and "[DONE]" in raw and "mock-small" in text,
      {"status": status, "chunks": len(chunks), "text": text})

status, raw, headers, elapsed = request(BASE + "/v1/responses",
    {"model": "switchyard/test", "input": "SIMPLE", "max_output_tokens": 128},
    {"x-switchyard-session-id": str(uuid.uuid4())})
body = json.loads(raw)
check("Responses API", status == 200 and body.get("model") == "mock-small",
      {"status": status, "model": body.get("model"), "body": body if status != 200 else None})

status, raw, _, _ = request(BASE + "/v1/responses",
    {"model": "switchyard/test", "input": "SIMPLE", "stream": True, "max_output_tokens": 128},
    {"x-switchyard-session-id": str(uuid.uuid4())})
events = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith("data: ") and line[6:] != "[DONE]"]
check("Responses streaming", status == 200 and any(e.get("type") == "response.completed" for e in events)
      and any(e.get("type") == "response.output_text.delta" and "mock-small" in e.get("delta", "") for e in events),
      {"status": status, "events": [e.get("type") for e in events]})

prefixed_session = "prefixed-" + str(uuid.uuid4())
before = judge_count()
for _ in range(2):
    status, raw, _, _ = request(BASE + "/v1/chat/completions",
        {"model": "switchyard/test", "messages": [user("SIMPLE")]},
        {"x-bf-eh-x-switchyard-session-id": prefixed_session})
    assert status == 200, raw
session_status, session_raw, _, _ = request("http://switchyard:4000/v1/routing/session-stats?session_id=" + prefixed_session)
session_stats = json.loads(session_raw)
check("prefixed session header", session_status == 200 and session_stats.get("session_id") == prefixed_session
      and session_stats.get("models", {}).get("mock-small", {}).get("calls") == 2,
      {"status": session_status, "session_stats": session_stats})

session = "tool-" + str(uuid.uuid4())
tools = [{"type": "function", "function": {"name": "lookup", "description": "Look up a key",
          "parameters": {"type": "object", "properties": {"key": {"type": "string"}}, "required": ["key"]}}}]
messages = [user("SIMPLE USE_TOOL")]
before = judge_count()
status, raw, _, _ = chat(messages, session, tools=tools)
body = json.loads(raw)
message = body.get("choices", [{}])[0].get("message", {})
check("tool call forwarded", status == 200 and bool(message.get("tool_calls")), {"status": status, "message": message})
if message.get("tool_calls"):
    first_count = judge_count()
    messages += [message, {"role": "tool", "tool_call_id": message["tool_calls"][0]["id"], "content": "COMPLEX tool data"}]
    status, raw, _, _ = chat(messages, session, tools=tools)
    body = json.loads(raw)
    check("tool continuation holds model", status == 200 and body.get("model") == "mock-small" and judge_count() == first_count,
          {"status": status, "model": body.get("model"), "judge_calls": judge_count() - before})
    messages += [body["choices"][0]["message"], user("COMPLEX new user task")]
    status, raw, _, _ = chat(messages, session)
    body = json.loads(raw)
    check("new user turn escalates", status == 200 and body.get("model") == "mock-big" and judge_count() == first_count + 1,
          {"status": status, "model": body.get("model"), "judge_calls": judge_count() - before})
    messages += [body["choices"][0]["message"], user("SIMPLE next user task")]
    status, raw, _, _ = chat(messages, session)
    body = json.loads(raw)
    check("later user turn de-escalates", status == 200 and body.get("model") == "mock-small" and judge_count() == first_count + 2,
          {"status": status, "model": body.get("model"), "judge_calls": judge_count() - before})

for label, prompt, expected_status in [("judge HTTP failure", "JUDGE_ERROR", 500),
                                       ("judge timeout", "JUDGE_TIMEOUT", 504)]:
    before_calls = len(json.loads(request("http://mock:9000/stats")[1]))
    status, raw, _, elapsed = chat([user(prompt)])
    after = json.loads(request("http://mock:9000/stats")[1])[before_calls:]
    check(label, status == expected_status and all(c["model"] == "mock-judge" for c in after),
          {"status": status, "elapsed_s": round(elapsed, 3), "models_called": [c["model"] for c in after], "body": json.loads(raw)})

for prompt in ["JUDGE_ERROR", "JUDGE_TIMEOUT"]:
    before_calls = len(json.loads(request("http://mock:9000/stats")[1]))
    status, raw, _, elapsed = chat([user(prompt)], fallbacks=["switchyard/test-big"])
    body = json.loads(raw)
    after = json.loads(request("http://mock:9000/stats")[1])[before_calls:]
    check("Bifrost fallback: " + prompt, status == 200 and body.get("model") == "mock-big"
          and [c["model"] for c in after] == ["mock-judge", "mock-big"],
          {"status": status, "model": body.get("model"), "elapsed_s": round(elapsed, 3),
           "models_called": [c["model"] for c in after]})

pathlib.Path("results").mkdir(exist_ok=True)
pathlib.Path("results/mock.json").write_text(json.dumps(results, indent=2) + "\n")
raise SystemExit(0 if all(r["passed"] for r in results) else 1)
