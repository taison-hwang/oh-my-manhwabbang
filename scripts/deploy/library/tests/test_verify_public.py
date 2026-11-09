import http.server
import os
import subprocess
import threading
import unittest


HERE = os.path.dirname(os.path.abspath(__file__))
VERIFY = os.path.join(os.path.dirname(HERE), "verify-public.sh")


class LoginPage(http.server.BaseHTTPRequestHandler):
    root_body = b""

    def log_message(self, _format, *_args):
        pass

    def send(self, status, body=b"", **headers):
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("CF-Cache-Status", "DYNAMIC")
        for name, value in headers.items():
            self.send_header(name.replace("_", "-"), value)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/":
            self.send(401, self.root_body, Content_Type="text/html")
        elif self.path.startswith("/api/health"):
            self.send(200, b'{"ok":true}', Content_Type="application/json")
        elif self.path == "/api/auth/status":
            self.send(200, b'{"mode":"oidc","authenticated":false}', Content_Type="application/json")
        elif self.path.startswith("/api/auth/oidc/start"):
            self.send(
                302,
                Location="https://auth.betterax.net/oauth2/auth",
                Set_Cookie="oidc_state=x; Secure; HttpOnly",
            )
        else:
            self.send(401, b'{"error":"unauthorized"}', Content_Type="application/json")

    def do_POST(self):
        self.send(403, b'{"error":"cross_origin"}', Content_Type="application/json")


class VerifyPublicRoot(unittest.TestCase):
    def run_verify(self, body):
        LoginPage.root_body = body
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), LoginPage)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        return subprocess.run(
            [VERIFY, "--base", f"http://127.0.0.1:{server.server_port}"],
            capture_output=True,
            text=True,
            timeout=10,
        )

    def test_cloudflare_injected_script_is_not_an_exposed_spa_asset(self):
        result = self.run_verify(
            b'<html>sign in<script defer src="https://static.cloudflareinsights.com/beacon.min.js"></script></html>'
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_product_spa_asset_is_rejected(self):
        result = self.run_verify(b'<html><script type="module" src="/assets/app.js"></script></html>')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("FAIL  X1", result.stdout)


if __name__ == "__main__":
    unittest.main()
