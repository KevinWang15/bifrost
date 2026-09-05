#!/usr/bin/env python3
"""Seed deterministic misses/hits and wait until Prometheus has scraped them."""

import json
import os
import time
from urllib.parse import urlencode
from urllib.request import Request, urlopen


BIFROST_URL = os.getenv("BIFROST_URL", "http://bifrost:8080").rstrip("/")
PROMETHEUS_URL = os.getenv("PROMETHEUS_URL", "http://prometheus:9090").rstrip("/")


def chat(user_id, prompt):
    payload = json.dumps(
        {"model": "openai/mock-model", "messages": [{"role": "user", "content": prompt}]},
        separators=(",", ":"),
    ).encode()
    request = Request(
        f"{BIFROST_URL}/v1/chat/completions",
        data=payload,
        method="POST",
        headers={
            "Content-Type": "application/json",
            "x-bf-cache-key": f"cache-report-demo-{user_id}",
            "x-bf-dim-user_id": user_id,
        },
    )
    with urlopen(request, timeout=15) as response:
        body = json.load(response)
    if not body.get("choices"):
        raise RuntimeError(f"unexpected Bifrost response: {body}")


def prometheus_value(expression):
    query = urlencode({"query": expression})
    with urlopen(f"{PROMETHEUS_URL}/api/v1/query?{query}", timeout=5) as response:
        payload = json.load(response)
    values = payload.get("data", {}).get("result", [])
    return sum(float(item["value"][1]) for item in values)


def wait_for(name, predicate, timeout=60):
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            if predicate():
                return
        except Exception as error:
            last_error = error
        time.sleep(1)
    raise RuntimeError(f"timed out waiting for {name}: {last_error or 'condition not met'}")


def endpoint_ready(url):
    with urlopen(url, timeout=3) as response:
        return response.status == 200


def repeated(user_id, prompt, count):
    chat(user_id, prompt)
    # Cache writes are asynchronous. Give the first miss time to persist before replaying it.
    time.sleep(0.75)
    for _ in range(count - 1):
        chat(user_id, prompt)


def main():
    wait_for("Bifrost", lambda: json.load(urlopen(f"{BIFROST_URL}/health", timeout=3)).get("status") == "ok")
    wait_for("Prometheus", lambda: endpoint_ready(f"{PROMETHEUS_URL}/-/ready"))

    run_id = time.time_ns()

    # Establish non-zero counter series, wait for a scrape, then generate the traffic
    # whose delta is used by the first report. Prometheus cannot infer the unseen zero
    # before a counter's first sample.
    repeated("alice", f"baseline-alice-{run_id}", 2)
    repeated("bob", f"baseline-bob-{run_id}", 2)
    wait_for("baseline cache counters", lambda: prometheus_value("sum(bifrost_cache_hits_total)") >= 2)

    # Measured traffic: Alice 3/4 hits, Bob 2/4 hits; aggregate 5/8 = 62.5%.
    repeated("alice", f"alice-daily-{run_id}", 4)
    repeated("bob", f"bob-daily-one-{run_id}", 2)
    repeated("bob", f"bob-daily-two-{run_id}", 2)
    wait_for(
        "measured cache counters",
        lambda: prometheus_value("sum(increase(bifrost_cache_hits_total[10m]))") >= 4.5
        and prometheus_value("sum(increase(bifrost_upstream_requests_total[10m]))") >= 7.5,
    )
    print("seeded cache metrics: expected measured hit rate is approximately 62.5%", flush=True)


if __name__ == "__main__":
    main()
