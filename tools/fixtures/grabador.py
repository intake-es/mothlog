import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit

OUT = Path(sys.argv[1])
OUT.mkdir(parents=True, exist_ok=True)


class Handler(BaseHTTPRequestHandler):
    def _cors(self):
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Headers", "*")
        self.send_header("Access-Control-Allow-Methods", "POST, OPTIONS")

    def do_OPTIONS(self):
        self.send_response(200)
        self._cors()
        self.end_headers()

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        n = len(list(OUT.glob("*.meta.json"))) + 1
        base = OUT / f"{n:03d}"
        base.with_suffix(".envelope").write_bytes(body)
        url = urlsplit(self.path)
        meta = {"method": "POST", "path": url.path, "query": url.query, "headers": dict(self.headers)}
        base.with_suffix(".meta.json").write_text(json.dumps(meta, indent=2, ensure_ascii=False))
        self.send_response(200)
        self._cors()
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, *a):
        pass


ThreadingHTTPServer(("127.0.0.1", 8299), Handler).serve_forever()
