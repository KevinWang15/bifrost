"""Small, paid OpenRouter integration experiment; run explicitly inside Docker."""
import json
import pathlib
import time
import urllib.error
import urllib.request
import uuid

BASE = "http://bifrost:8080"
SWITCHYARD = "http://switchyard:4000"
OUTPUT = pathlib.Path("results")


def fetch(url, payload=None, headers=None):
    req = urllib.request.Request(url, data=None if payload is None else json.dumps(payload).encode(),
                                 headers={"Content-Type": "application/json", **(headers or {})})
    started = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=190) as response:
            return response.status, json.load(response), dict(response.headers), time.monotonic() - started
    except urllib.error.HTTPError as error:
        return error.code, {"error": error.read().decode()}, {}, time.monotonic() - started


def usage():
    key = pathlib.Path("/run/secrets/openrouter_api_key").read_text().strip()
    status, body, _, _ = fetch("https://openrouter.ai/api/v1/key", headers={"Authorization": "Bearer " + key})
    return body["data"]["usage"] if status == 200 else None


def stats():
    return fetch(SWITCHYARD + "/v1/stats")[1]


def chat(prompt, route="smart", session=None, history=None, **options):
    messages = history or [{"role": "user", "content": prompt}]
    status, body, headers, elapsed = fetch(BASE + "/v1/chat/completions",
        {"model": "switchyard/" + route, "messages": messages, "max_tokens": 512, "temperature": 0, **options},
        {"x-switchyard-session-id": session or str(uuid.uuid4())})
    return {"status": status, "elapsed_s": round(elapsed, 3), "headers": headers, "response": body}


def main():
    OUTPUT.mkdir(exist_ok=True)
    start_usage = usage()
    results = {"usage_before": start_usage, "stats_before": stats(), "cases": []}
    cases = [
        ("extract", 'Extract name and age from "Ada is 37 years old." Return only JSON with name and age keys.'),
        ("chinese", "把‘Good morning, see you tomorrow.’翻译成中文。只输出译文。"),
        ("bounded_code", "Write a Python function is_palindrome(s) that compares the string exactly, including case and punctuation. Tests: '' -> True; 'aba' -> True; 'ab' -> False; 'Aa' -> False. Return only the function."),
        ("closure_bug", "Explain the bug in Python fs = [lambda: i for i in range(3)]; print([f() for f in fs]). Give the actual output and a minimal fix. Be concise."),
        ("concurrency", "Design a bounded multi-producer multi-consumer queue in C++20 using atomics only. Explain linearization points, memory ordering, ABA prevention, wraparound, and a concrete adversarial interleaving that breaks a naive implementation. Distinguish safety from lock-free progress; do not assert a proof you have not justified. Limit the answer to 300 words."),
        ("algorithm", "For a directed graph with integer edge weights, possibly negative, give an algorithm to determine for every pair (u,v) whether the shortest-path distance is finite, +infinity, or -infinity. Specify how negative cycles affect pairs, justify correctness, and analyze time and space. Be precise and stay within 300 words."),
    ]
    for name, prompt in cases:
        result = {"name": name, "prompt": prompt, **chat(prompt)}
        results["cases"].append(result)
        body = result["response"]
        print(json.dumps({"name": name, "status": result["status"], "model": body.get("model"),
                          "elapsed_s": result["elapsed_s"], "error": body.get("error")}), flush=True)
        OUTPUT.joinpath("live.json").write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n")
        if result["status"] != 200:
            break

    if all(c["status"] == 200 for c in results["cases"]):
        # Same prompt on fixed routes, for transport and response baselines.
        for route in ["small", "big"]:
            result = {"name": "baseline_" + route, **chat(cases[0][1], route=route)}
            results["cases"].append(result)
            print(json.dumps({"name": result["name"], "status": result["status"],
                              "model": result["response"].get("model"), "elapsed_s": result["elapsed_s"]}), flush=True)

        status, body, headers, elapsed = fetch(BASE + "/v1/responses",
            {"model": "switchyard/smart", "input": "Return exactly the word READY.", "max_output_tokens": 128},
            {"x-switchyard-session-id": str(uuid.uuid4())})
        results["cases"].append({"name": "responses", "status": status, "elapsed_s": round(elapsed, 3), "response": body})
        print(json.dumps({"name": "responses", "status": status, "model": body.get("model")}), flush=True)

        req = urllib.request.Request(BASE + "/v1/chat/completions",
            data=json.dumps({"model": "switchyard/smart", "messages": [{"role": "user", "content": "Count from one to five."}],
                             "stream": True, "stream_options": {"include_usage": True}, "max_tokens": 128}).encode(),
            headers={"Content-Type": "application/json", "x-switchyard-session-id": str(uuid.uuid4())})
        start = time.monotonic()
        stream = {"name": "stream", "chunks": [], "first_content_s": None, "done": False}
        try:
            with urllib.request.urlopen(req, timeout=190) as response:
                stream["status"] = response.status
                for line in response:
                    text = line.decode().strip()
                    if text == "data: [DONE]":
                        stream["done"] = True
                    elif text.startswith("data: "):
                        chunk = json.loads(text[6:])
                        stream["chunks"].append(chunk)
                        if any(c.get("delta", {}).get("content") for c in chunk.get("choices", [])) and stream["first_content_s"] is None:
                            stream["first_content_s"] = round(time.monotonic() - start, 3)
        except urllib.error.HTTPError as error:
            stream.update(status=error.code, error=error.read().decode())
        stream["elapsed_s"] = round(time.monotonic() - start, 3)
        results["cases"].append(stream)
        print(json.dumps({k: v for k, v in stream.items() if k != "chunks"}), flush=True)

    results["stats_after"] = stats()
    results["usage_after"] = usage()
    if start_usage is not None and results["usage_after"] is not None:
        results["account_usage_delta_usd"] = round(results["usage_after"] - start_usage, 8)
    OUTPUT.joinpath("live.json").write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n")
    if not all(c["status"] == 200 and c.get("done", True) for c in results["cases"]):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
