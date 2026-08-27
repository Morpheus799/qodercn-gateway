#!/usr/bin/env python3
"""Measure the cadence of Anthropic /v1/messages streaming from the proxy.

Prints, per text_delta frame: t_ms since first byte, gap_ms since prev delta,
rune length of the delta. Ends with a summary (count, total chars, gap stats,
delta-size stats) so we can tell smooth (many small ~even-gap deltas) from
chunky (few big deltas / long gaps).
"""
import sys, json, time, http.client, statistics

KEY = open("/home/ubuntu/.config/lingma-proxy/auth-keys.txt").read()
KEY = [l for l in KEY.splitlines() if l and not l.startswith("#")][0].strip()

model = sys.argv[1] if len(sys.argv) > 1 else "Qwen3.7-Flash"
with_tools = (len(sys.argv) > 2 and sys.argv[2] == "tools")
prompt = "请写一段约300字的中文散文，主题是清晨的海边。只写散文本身，不要工具调用。"

body = {
    "model": model,
    "max_tokens": 1024,
    "stream": True,
    "messages": [{"role": "user", "content": prompt}],
}
if with_tools:
    body["tools"] = [{
        "name": "get_weather",
        "description": "Get weather for a city",
        "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]},
    }]

conn = http.client.HTTPConnection("127.0.0.1", 8095, timeout=120)
conn.request("POST", "/v1/messages", body=json.dumps(body),
             headers={"x-api-key": KEY, "content-type": "application/json", "anthropic-version": "2023-06-01"})
resp = conn.getresponse()
print(f"# HTTP {resp.status}  model={model}  tools={with_tools}", flush=True)

t0 = None
last = None
gaps = []
sizes = []
buf = b""
event = None
n = 0
while True:
    chunk = resp.read(1)          # byte-at-a-time so our own read never batches frames
    if not chunk:
        break
    buf += chunk
    if not buf.endswith(b"\n\n"):
        continue
    now = time.monotonic()
    raw = buf.decode("utf-8", "replace")
    buf = b""
    for line in raw.splitlines():
        if line.startswith("event:"):
            event = line[6:].strip()
        elif line.startswith("data:"):
            data = line[5:].strip()
            if event == "content_block_delta":
                try:
                    d = json.loads(data)
                except Exception:
                    continue
                delta = d.get("delta", {})
                txt = delta.get("text") or delta.get("thinking") or delta.get("partial_json") or ""
                if not txt:
                    continue
                if t0 is None:
                    t0 = now
                    last = now
                gap = (now - last) * 1000
                last = now
                tms = (now - t0) * 1000
                gaps.append(gap)
                sizes.append(len(txt))
                n += 1
                print(f"[{tms:8.1f}ms] +{gap:7.1f}ms  {len(txt):4d} runes  {txt[:40]!r}", flush=True)

def stats(xs):
    if not xs:
        return "n/a"
    return f"min={min(xs):.1f} p50={statistics.median(xs):.1f} mean={statistics.mean(xs):.1f} max={max(xs):.1f}"

print(f"\n# deltas={n}  total_runes={sum(sizes)}")
print(f"# gap_ms:   {stats(gaps[1:])}")   # skip first gap (=0)
print(f"# size_rune:{stats(sizes)}")
