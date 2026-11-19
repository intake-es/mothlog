import gzip, json, sys, zlib
from pathlib import Path

def decode(raw, enc):
    if enc == "gzip": return gzip.decompress(raw)
    if enc == "deflate": return zlib.decompress(raw)
    if enc == "br":
        import brotli; return brotli.decompress(raw)
    return raw

def items(data):
    lines = data.split(b"\n", 1)
    head = json.loads(lines[0]); rest = lines[1] if len(lines) > 1 else b""
    out = []
    while rest.strip():
        h, rest = rest.split(b"\n", 1)
        h = json.loads(h)
        if "length" in h:
            payload, rest = rest[: h["length"]], rest[h["length"] + 1 :]
        else:
            payload, _, rest = rest.partition(b"\n")
        out.append((h, payload))
    return head, out

for meta_path in sorted(Path(sys.argv[1]).glob("*.meta.json")):
    meta = json.loads(meta_path.read_text())
    raw = meta_path.with_name(meta_path.name.replace(".meta.json", ".envelope")).read_bytes()
    hdr = {k.lower(): v for k, v in meta["headers"].items()}
    data = decode(raw, hdr.get("content-encoding"))
    head, its = items(data)
    desc = []
    for h, p in its:
        t = h.get("type")
        if t == "event":
            e = json.loads(p)
            exc = (e.get("exception") or {}).get("values") or []
            msg = (e.get("logentry") or {}).get("formatted") or e.get("message")
            desc.append(f"event[{e.get('level')}] exc={[x.get('type') for x in exc]} msg={str(msg)[:60]!r} logger={e.get('logger')} fp={e.get('fingerprint')} tags={e.get('tags')}")
        else:
            desc.append(f"{t} ({len(p)} B)")
    print(f"{meta_path.name.replace('.meta.json',''):38} {meta['path']:24} enc={hdr.get('content-encoding','-'):5} ua={hdr.get('user-agent','')[:22]:22} " + " | ".join(desc))
