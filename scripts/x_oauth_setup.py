#!/usr/bin/env python3
import argparse
import base64
import hashlib
import http.server
import json
import os
import secrets
import stat
import sys
import threading
import urllib.parse
import urllib.request
from getpass import getpass
from pathlib import Path


AUTH_URL = "https://x.com/i/oauth2/authorize"
TOKEN_URL = "https://api.x.com/2/oauth2/token"


class CallbackHandler(http.server.BaseHTTPRequestHandler):
    server_version = "BacklightXOAuth/1.0"

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path != self.server.callback_path:
            self.send_error(404)
            return
        params = urllib.parse.parse_qs(parsed.query)
        self.server.auth_code = first(params.get("code"))
        self.server.auth_state = first(params.get("state"))
        self.server.auth_error = first(params.get("error"))
        if self.server.auth_code:
            body = b"X OAuth code received. You can return to Codex."
            self.send_response(200)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        else:
            body = b"X OAuth callback did not include a code. Return to Codex for details."
            self.send_response(400)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        threading.Thread(target=self.server.shutdown, daemon=True).start()

    def log_message(self, fmt, *args):
        return


def first(values):
    if not values:
        return ""
    return values[0]


def b64url(data):
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def pkce_pair():
    verifier = b64url(secrets.token_bytes(48))
    challenge = b64url(hashlib.sha256(verifier.encode("ascii")).digest())
    return verifier, challenge


def build_authorize_url(client_id, redirect_uri, scope, state, code_challenge):
    query = urllib.parse.urlencode(
        {
            "response_type": "code",
            "client_id": client_id,
            "redirect_uri": redirect_uri,
            "scope": " ".join(scope),
            "state": state,
            "code_challenge": code_challenge,
            "code_challenge_method": "S256",
        },
        quote_via=urllib.parse.quote,
    )
    return f"{AUTH_URL}?{query}"


def exchange_code(client_id, client_secret, redirect_uri, code, verifier):
    body = urllib.parse.urlencode(
        {
            "code": code,
            "grant_type": "authorization_code",
            "redirect_uri": redirect_uri,
            "code_verifier": verifier,
        }
    ).encode("utf-8")
    req = urllib.request.Request(TOKEN_URL, data=body, method="POST")
    req.add_header("Content-Type", "application/x-www-form-urlencoded")
    if client_secret:
        basic = base64.b64encode(f"{client_id}:{client_secret}".encode("utf-8")).decode("ascii")
        req.add_header("Authorization", f"Basic {basic}")
    else:
        # Public clients must include client_id in the token request body.
        body = urllib.parse.urlencode(
            {
                "code": code,
                "grant_type": "authorization_code",
                "redirect_uri": redirect_uri,
                "code_verifier": verifier,
                "client_id": client_id,
            }
        ).encode("utf-8")
        req.data = body
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as err:
        detail = err.read().decode("utf-8", errors="replace")
        raise SystemExit(f"token exchange failed: HTTP {err.code}: {detail}") from err


def write_tokens(path, tokens, client_id, redirect_uri):
    payload = {
        "client_id": client_id,
        "redirect_uri": redirect_uri,
        "token_type": tokens.get("token_type"),
        "expires_in": tokens.get("expires_in"),
        "scope": tokens.get("scope"),
        "access_token": tokens.get("access_token"),
        "refresh_token": tokens.get("refresh_token"),
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    os.chmod(path, stat.S_IRUSR | stat.S_IWUSR)


def main():
    parser = argparse.ArgumentParser(description="Run local X OAuth 2.0 setup for Backlight publishing.")
    parser.add_argument("--client-id", default=os.environ.get("X_CLIENT_ID", ""))
    parser.add_argument("--client-secret", default=os.environ.get("X_CLIENT_SECRET", ""))
    parser.add_argument("--redirect-uri", default=os.environ.get("X_REDIRECT_URI", "http://127.0.0.1:8787/callback"))
    parser.add_argument(
        "--scope",
        nargs="+",
        default=["tweet.read", "tweet.write", "users.read", "media.write", "offline.access"],
    )
    parser.add_argument("--token-output", type=Path, default=Path("/private/tmp/backlight_x_oauth_tokens.json"))
    args = parser.parse_args()

    client_id = args.client_id.strip() or input("X Client ID: ").strip()
    client_secret = args.client_secret.strip()
    if not client_secret:
        client_secret = getpass("X Client Secret (blank for public client): ").strip()

    parsed_redirect = urllib.parse.urlparse(args.redirect_uri)
    host = parsed_redirect.hostname or "127.0.0.1"
    port = parsed_redirect.port or (443 if parsed_redirect.scheme == "https" else 80)
    path = parsed_redirect.path or "/callback"

    verifier, challenge = pkce_pair()
    state = secrets.token_urlsafe(24)
    url = build_authorize_url(client_id, args.redirect_uri, args.scope, state, challenge)

    server = http.server.HTTPServer((host, port), CallbackHandler)
    server.callback_path = path
    server.auth_code = ""
    server.auth_state = ""
    server.auth_error = ""

    print("\nOpen this URL in your browser and approve the X app:\n")
    print(url)
    print(f"\nWaiting for callback on {args.redirect_uri} ...", flush=True)
    server.serve_forever()

    if server.auth_error:
        raise SystemExit(f"authorization failed: {server.auth_error}")
    if not server.auth_code:
        raise SystemExit("authorization failed: callback did not include code")
    if server.auth_state != state:
        raise SystemExit("authorization failed: state mismatch")

    tokens = exchange_code(client_id, client_secret, args.redirect_uri, server.auth_code, verifier)
    write_tokens(args.token_output, tokens, client_id, args.redirect_uri)

    print("\nToken exchange succeeded.")
    print(f"Saved tokens to: {args.token_output}")
    print("Add X_REFRESH_TOKEN from that file to Backlight env after confirming the test account.")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit("\nInterrupted.")
