# A made-up companion app's back-channel logout endpoint, for the stack
# checks only (deploy/test/companion.yaml, SPEC.md A150). It keeps every
# POST Authentik sends, in memory, and lists them as JSON on GET /received:
# the path, the logout token, and when it came. It checks nothing: the
# checks verify the tokens themselves.
import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Lock
from urllib.parse import parse_qs

received = []
lock = Lock()


class Receiver(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0)).decode("utf-8", "replace")
        token = parse_qs(body).get("logout_token", [""])[0]
        with lock:
            received.append({"path": self.path, "logout_token": token, "at": time.time()})
        self.send_response(200)
        self.send_header("Cache-Control", "no-store")
        self.end_headers()

    def do_GET(self):
        if self.path != "/received":
            self.send_error(404)
            return
        with lock:
            data = json.dumps(received).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


ThreadingHTTPServer(("0.0.0.0", 8026), Receiver).serve_forever()
