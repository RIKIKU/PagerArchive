#!/usr/bin/env python3
"""ADR 0009 probe: the PowerShell module's OAuth client against a THROWAWAY test
stack (it creates users, edits token rows and restarts Authentik). Run from the
repo root after stack_up (scripts/stack-lib.sh), with deploy/test/stack.vars in
the environment and COMPOSE_PROJECT_NAME set: python3 scripts/oauth-probe.py

Proves, for the PowerShell module's public client: PKCE with a loopback
redirect on a random port, the device code flow, refresh tokens renewed on
use and ending after 30 days unused, revocation, and what deactivation,
deletion, the browser session ending and an Authentik restart do.
Tokens are never printed: only their length, claims and outcomes.
"""
import base64, hashlib, json, os, secrets, subprocess, sys, time, urllib.parse
import requests

AK = "http://localhost:9000"
CLIENT = "pagerarchive-powershell"
ISSUER = f"{AK}/application/o/pagerarchive-powershell/"
SCOPE = "openid email profile offline_access"
API_TOKEN = os.environ["AUTHENTIK_API_TOKEN"]
COMPOSE = ["docker", "compose", "-p", os.environ.get("COMPOSE_PROJECT_NAME", "pa-m8exp"), "-f", "compose.yaml", "-f", "deploy/test/compose.yaml"]

def say(*a):
    print(*a, flush=True)

def check(name, ok, detail=""):
    say(("PASS " if ok else "FAIL ") + name + (f"  ({detail})" if detail else ""))
    if not ok:
        global failures
        failures += 1

failures = 0

def sh(args, **kw):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kw).stdout

def ak_shell(code):
    return sh(COMPOSE + ["exec", "-T", "authentik-worker", "ak", "shell", "-c", code]).strip().splitlines()[-1]

def sql(q):
    return sh(COMPOSE + ["exec", "-T", "db", "psql", "-U", "postgres", "-d", "authentik", "-tAc", q]).strip()

def make_user(email):
    pw = "correct horse " + email
    pk = ak_shell(
        "from authentik.core.models import User\n"
        f"u,_=User.objects.get_or_create(username={email!r}, defaults=dict(email={email!r}, name='Test', path='users/pagerarchive'))\n"
        f"u.is_active=True; u.set_password({pw!r}); u.save(); print(u.pk)")
    return int(pk), pw

def claims(jwt):
    p = jwt.split(".")[1]
    return json.loads(base64.urlsafe_b64decode(p + "=" * (-len(p) % 4)))

def header(jwt):
    p = jwt.split(".")[0]
    return json.loads(base64.urlsafe_b64decode(p + "=" * (-len(p) % 4)))

class Browser:
    """A cookie jar driving Authentik's flows through the flow executor API."""
    def __init__(self, email, pw):
        self.s = requests.Session()
        self.email, self.pw = email, pw

    def executor(self, slug, query, body=None):
        u = f"{AK}/api/v3/flows/executor/{slug}/?query=" + urllib.parse.quote(query)
        for _ in range(5):
            if body is None:
                r = self.s.get(u, headers={"Accept": "application/json"}, allow_redirects=False)
            else:
                # Signed in, the executor wants Authentik's CSRF header, as its web UI sends.
                r = self.s.post(u, json=body, allow_redirects=False, headers={
                    "Accept": "application/json", "X-authentik-CSRF": self.s.cookies.get("authentik_csrf", "")})
            if r.status_code == 302:
                body = None
                continue
            return r.json()
        raise RuntimeError("executor loop")

    def answer(self, c, extra):
        comp = c["component"]
        if c.get("response_errors") or c.get("error_message"):
            raise RuntimeError(f"flow error at {comp}: {c.get('response_errors') or c.get('error_message')}")
        if comp == "ak-stage-identification":
            return {"uid_field": self.email, "password": self.pw}
        if comp == "ak-stage-password":
            return {"password": self.pw}
        if comp in extra:
            return extra[comp]
        return None

    def walk(self, start, stop_host, extra=None):
        """Follow redirects; run flows; stop at the first URL on stop_host."""
        extra = extra or {}
        nxt = start
        for _ in range(40):
            u = urllib.parse.urlparse(nxt)
            if u.netloc == stop_host:
                return nxt
            if u.path.startswith("/if/flow/"):
                slug = u.path.split("/")[3]
                c = self.executor(slug, u.query)
                while c["component"] != "xak-flow-redirect":
                    body = self.answer(c, extra)
                    if body is None:
                        return f"AT {c['component']}: {json.dumps(c)[:300]}"
                    body["component"] = c["component"]
                    c = self.executor(slug, u.query, body)
                nxt = urllib.parse.urljoin(nxt, c["to"])
                continue
            r = self.s.get(nxt, allow_redirects=False)
            loc = r.headers.get("Location")
            if not (300 <= r.status_code < 400 and loc):
                return f"STOPPED {r.status_code} {nxt}"
            nxt = urllib.parse.urljoin(nxt, loc)
        raise RuntimeError("too many redirects")

def pkce():
    v = secrets.token_urlsafe(48)
    c = base64.urlsafe_b64encode(hashlib.sha256(v.encode()).digest()).rstrip(b"=").decode()
    return v, c

def authorize(b, redirect, challenge=None, stop_host=None):
    q = {"client_id": CLIENT, "response_type": "code", "redirect_uri": redirect, "scope": SCOPE, "state": "st8"}
    if challenge:
        q.update(code_challenge=challenge, code_challenge_method="S256")
    end = b.walk(f"{AK}/application/o/authorize/?" + urllib.parse.urlencode(q),
                 stop_host or urllib.parse.urlparse(redirect).netloc)
    if end.startswith(("STOPPED", "AT ")):
        return None, end
    qs = urllib.parse.parse_qs(urllib.parse.urlparse(end).query)
    return qs.get("code", [None])[0], qs

def token(data):
    data = dict(data, client_id=CLIENT)
    r = requests.post(f"{AK}/application/o/token/", data=data)
    return r.status_code, r.json()

def refresh(rt):
    return token({"grant_type": "refresh_token", "refresh_token": rt})

def rt_row(rt):
    h = rt.replace("'", "")
    return sql(f"select to_char(expires at time zone 'UTC','YYYY-MM-DD HH24:MI'), revoked, session_id is not null "
               f"from authentik_providers_oauth2_refreshtoken where token='{h}'")

def main():
    disc = requests.get(ISSUER + ".well-known/openid-configuration").json()
    say("discovery:", {k: disc[k] for k in ("issuer", "token_endpoint", "revocation_endpoint", "device_authorization_endpoint", "jwks_uri")})
    say("  code_challenge_methods_supported:", disc.get("code_challenge_methods_supported"), " grant_types_supported:", disc.get("grant_types_supported"))

    pk, pw = make_user("m8-pkce@example.test")
    b = Browser("m8-pkce@example.test", pw)

    say("\n== E1 PKCE with a loopback redirect on a random port (RFC 8252)")
    port = secrets.randbelow(20000) + 40000
    v, c = pkce()
    code, _ = authorize(b, f"http://127.0.0.1:{port}/", c)
    check(f"code issued to http://127.0.0.1:{port}/", code is not None)
    st, wrong = token({"grant_type": "authorization_code", "code": code, "redirect_uri": f"http://127.0.0.1:{port}/", "code_verifier": "x" * 50})
    check("exchange with the wrong code_verifier refused", st == 400, f"{st} {wrong.get('error')}")
    v, c = pkce()
    code, _ = authorize(b, f"http://127.0.0.1:{port}/", c)
    st, tok = token({"grant_type": "authorization_code", "code": code, "redirect_uri": f"http://127.0.0.1:{port}/", "code_verifier": v})
    check("exchange with the right code_verifier gives tokens", st == 200 and "access_token" in tok and "refresh_token" in tok,
          f"{st}, expires_in={tok.get('expires_in')}, refresh_token length {len(tok.get('refresh_token',''))}")
    st, again = token({"grant_type": "authorization_code", "code": code, "redirect_uri": f"http://127.0.0.1:{port}/", "code_verifier": v})
    check("the same code a second time refused", st == 400, f"{st} {again.get('error')}")
    port2 = secrets.randbelow(20000) + 40000
    v2, c2 = pkce()
    code2, _ = authorize(b, f"http://127.0.0.1:{port2}/", c2)
    check(f"another random port ({port2}) also accepted", code2 is not None)
    for bad in ("http://localhost:%d/" % port, "http://127.0.0.1.example.test:%d/" % port, "https://example.test/", "http://127.0.0.1:%d/x" % port):
        code3, info = authorize(b, bad, c2, stop_host="__never__")
        check(f"redirect {bad} refused", code3 is None, str(info)[:90])
    nopkce, info = authorize(b, f"http://127.0.0.1:{port}/")
    check("authorize without a code_challenge refused (blueprint policy)", nopkce is None, str(info)[:160])
    q = {"client_id": CLIENT, "response_type": "code", "redirect_uri": f"http://127.0.0.1:{port}/", "scope": SCOPE,
         "state": "st8", "code_challenge": "a" * 43, "code_challenge_method": "plain"}
    plain = b.walk(f"{AK}/application/o/authorize/?" + urllib.parse.urlencode(q), f"127.0.0.1:{port}")
    check("authorize with the plain method refused", "code=" not in plain, plain[:120])

    say("\n== E2 the access token")
    at = tok["access_token"]
    cl, hd = claims(at), header(at)
    say("  header:", {k: hd[k] for k in ("alg", "kid", "typ") if k in hd})
    say("  claims:", {k: cl[k] for k in ("iss", "aud", "azp", "sub", "scope") if k in cl}, " exp-iat =", cl["exp"] - cl["iat"])
    check("issuer is the module's application", cl["iss"] == ISSUER)
    check("audience is the module's client", cl["aud"] == CLIENT)
    check("subject is the Authentik user id (sub_mode user_id)", cl["sub"] == str(pk), cl["sub"])
    jwks = requests.get(disc["jwks_uri"]).json()
    check("kid is in the issuer's JWKS", any(k["kid"] == hd["kid"] for k in jwks["keys"]))
    idc = claims(tok["id_token"])
    say("  id_token differs by:", sorted(set(cl) ^ set(idc)))

    say("\n== E3 refresh renews the refresh token (threshold 0)")
    rt = tok["refresh_token"]
    say("  first refresh token:", rt_row(rt))
    st, r1 = refresh(rt)
    check("refresh gives a new access token and a new refresh token", st == 200 and r1.get("refresh_token") not in (None, rt), str(st))
    say("  old token now:", rt_row(rt), "  new token:", rt_row(r1["refresh_token"]))
    st, r0 = refresh(rt)
    check("the old refresh token is refused after rotation", st == 400 and r0.get("error") == "invalid_grant", f"{st} {r0.get('error')}")
    rt = r1["refresh_token"]

    say("\n== E4 30 days unused")
    sql(f"update authentik_providers_oauth2_refreshtoken set expires = now() + interval '1 day' where token='{rt}'")
    say("  made 29 days old:", rt_row(rt))
    st, r2 = refresh(rt)
    check("a 29-day-old refresh token still renews", st == 200, str(st))
    rt = r2["refresh_token"]
    say("  renewed token:", rt_row(rt))
    sql(f"update authentik_providers_oauth2_refreshtoken set expires = now() - interval '1 minute' where token='{rt}'")
    st, r3 = refresh(rt)
    check("a refresh token unused for 30 days is refused", st == 400 and r3.get("error") == "invalid_grant", f"{st} {r3.get('error')}")

    say("\n== E5 the browser's Authentik session ends")
    v, c = pkce()
    code, _ = authorize(b, f"http://127.0.0.1:{port}/", c)
    st, tok = token({"grant_type": "authorization_code", "code": code, "redirect_uri": f"http://127.0.0.1:{port}/", "code_verifier": v})
    rt = tok["refresh_token"]
    say("  refresh token linked to a session:", rt_row(rt))
    n = ak_shell(f"from authentik.core.models import AuthenticatedSession as A\nprint(A.objects.filter(user__pk={pk}).delete()[0])")
    say("  deleted the user's Authentik sessions:", n, " token now:", rt_row(rt))
    st, r4 = refresh(rt)
    check("refresh still works after the browser session ended", st == 200, str(st))
    rt = r4["refresh_token"]

    say("\n== E6 revocation (RFC 7009), public client, no secret")
    r = requests.post(disc["revocation_endpoint"], data={"client_id": CLIENT, "token": rt, "token_type_hint": "refresh_token"})
    check("revoke answers 200", r.status_code == 200, str(r.status_code))
    say("  token row after revoke:", rt_row(rt) or "(deleted)")
    st, r5 = refresh(rt)
    check("a revoked refresh token is refused", st == 400 and r5.get("error") == "invalid_grant", f"{st} {r5.get('error')}")
    r = requests.post(disc["revocation_endpoint"], data={"token": rt})
    say("  revoke without client_id:", r.status_code)

    say("\n== E7 device code (RFC 8628)")
    pk2, pw2 = make_user("m8-device@example.test")
    r = requests.post(disc["device_authorization_endpoint"], data={"client_id": CLIENT, "scope": SCOPE})
    d = r.json()
    check("device authorization gives a user code and a verification URI", r.status_code == 200 and "user_code" in d,
          f"verification_uri={d.get('verification_uri')}, expires_in={d.get('expires_in')}, interval={d.get('interval')}")
    st, pend = token({"grant_type": "urn:ietf:params:oauth:grant-type:device_code", "device_code": d["device_code"]})
    check("polling before the code is entered: authorization_pending", pend.get("error") == "authorization_pending", f"{st} {pend.get('error')}")
    other = Browser("m8-device@example.test", pw2)  # another device, not signed in
    end = other.walk(d["verification_uri"], "__never__", extra={"ak-provider-oauth2-device-code": {"code": d["user_code"]}})
    say("  browser on the other device ended at:", end[:300])
    st, dt = token({"grant_type": "urn:ietf:params:oauth:grant-type:device_code", "device_code": d["device_code"]})
    check("after sign-in and the code: tokens, with a refresh token", st == 200 and "refresh_token" in dt, str(st))
    check("device token's subject is that user", claims(dt["access_token"])["sub"] == str(pk2))
    st, dt2 = token({"grant_type": "urn:ietf:params:oauth:grant-type:device_code", "device_code": d["device_code"]})
    check("the device code can't be used twice", st != 200, f"{st} {dt2.get('error')}")
    st, dr = refresh(dt["refresh_token"])
    check("device sign-in's refresh token renews", st == 200, str(st))
    drt = dr["refresh_token"]

    say("\n== E8 deactivation in Authentik (the app's SetActive)")
    v, c = pkce()
    code, _ = authorize(b, f"http://127.0.0.1:{port}/", c)
    st, tok = token({"grant_type": "authorization_code", "code": code, "redirect_uri": f"http://127.0.0.1:{port}/", "code_verifier": v})
    rt = tok["refresh_token"]
    h = {"Authorization": f"Bearer {API_TOKEN}"}
    r = requests.patch(f"{AK}/api/v3/core/users/{pk}/", json={"is_active": False}, headers=h)
    say("  disable with the app's API token:", r.status_code, " token row:", rt_row(rt) or "(deleted)")
    st, r6 = refresh(rt)
    check("a deactivated user's refresh token is refused", st == 400, f"{st} {r6.get('error')}")
    requests.patch(f"{AK}/api/v3/core/users/{pk}/", json={"is_active": True}, headers=h)
    st, r7 = refresh(rt)
    check("and stays refused after reactivation", st == 400, f"{st} {r7.get('error')}")

    say("\n== E9 Authentik restart")
    kids = sorted(k["kid"] for k in requests.get(disc["jwks_uri"]).json()["keys"])
    t0 = time.time()
    sh(COMPOSE + ["restart", "authentik-server", "authentik-worker"])
    for _ in range(120):
        try:
            if requests.get(ISSUER + ".well-known/openid-configuration", timeout=3).status_code == 200:
                break
        except requests.RequestException:
            pass
        time.sleep(2)
    say(f"  restarted, discovery answers after {time.time()-t0:.0f} s")
    check("signing key ids unchanged", kids == sorted(k["kid"] for k in requests.get(disc["jwks_uri"]).json()["keys"]), str(kids))
    st, r8 = refresh(drt)
    check("a refresh token from before the restart still renews", st == 200, str(st))

    say("\n== E10 account deletion in Authentik (the app's DeleteUser)")
    drt = r8["refresh_token"]
    r = requests.delete(f"{AK}/api/v3/core/users/{pk2}/", headers=h)
    say("  delete with the app's API token:", r.status_code)
    st, r9 = refresh(drt)
    check("a deleted user's refresh token is refused", st == 400, f"{st} {r9.get('error')}")

    say(f"\n{failures} failed")
    return 1 if failures else 0

if __name__ == "__main__":
    sys.exit(main())
