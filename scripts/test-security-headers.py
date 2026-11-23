#!/usr/bin/env python3
"""Test nginx templates in isolated containers; requires Docker, curl and OpenSSL."""

import os
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parents[1]
IMAGE = os.environ.get("NGINX_TEST_IMAGE", "nginx:1.27-alpine")
HEADERS = {
    "x-content-type-options": "nosniff",
    "x-frame-options": "DENY",
    "referrer-policy": "strict-origin-when-cross-origin",
    "permissions-policy": "camera=(), microphone=(), geolocation=()",
}


def run(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.STDOUT).strip()


def response(url, *args):
    output = run("curl", "-ksS", "--retry", "10", "--retry-connrefused",
                 "--retry-delay", "1", "--max-time", "15", *args, url)
    lines = output.replace("\r", "").splitlines()
    status = int(lines[0].split()[1])
    headers = {}
    for line in lines[1:]:
        if not line:
            break
        name, value = line.split(":", 1)
        headers.setdefault(name.lower(), []).append(value.strip())
    return status, headers


def check(url, status, policy, *args):
    actual, headers = response(url, *(args or ("-I",)))
    assert actual == status, (url, actual, status)
    expected = {**HEADERS, "content-security-policy": policy.replace("$http_host", urlsplit(url).netloc)}
    for name, value in expected.items():
        assert headers.get(name) == [value], (url, name, headers.get(name))
    return headers


def main():
    gateway = (ROOT / "nginx/default.conf").read_text()
    host = (ROOT / "nginx/host-site.conf").read_text()
    web = (ROOT / "app/nginx.conf").read_text()
    policy = re.search(r'add_header Content-Security-Policy "([^"]+)" always;', gateway)[1]
    for source in (gateway, host, web):
        policies = re.findall(r'add_header Content-Security-Policy "([^"]+)" always;', source)
        assert policies and all(value == policy for value in policies), "CSP templates diverged"
    assert "frame-ancestors 'none'" in policy and "object-src 'none'" in policy
    scripts = re.search(r"script-src ([^;]+)", policy)[1]
    assert "'unsafe-inline'" not in scripts and "'unsafe-eval'" not in scripts
    assert "Strict-Transport-Security" not in gateway + web

    with tempfile.TemporaryDirectory(prefix="security-headers-") as directory:
        fixtures = Path(directory)
        fixtures.chmod(0o755)
        (fixtures / "index.html").write_text("<!doctype html><title>Header fixture</title>")
        (fixtures / "bundle.js").write_text("console.log('header fixture');")
        (fixtures / "oversized").write_bytes(b"x" * (2 << 20))
        run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
            "-subj", "/CN=example.com", "-keyout", str(fixtures / "privkey.pem"),
            "-out", str(fixtures / "fullchain.pem"))
        web = web.replace("listen 80;", "listen 3000;").replace("/usr/share/nginx/html", "/fixtures")
        mocks = """
server {
    listen 8080;
    add_header X-Content-Type-Options nosniff always;
    default_type application/json;
    location /api/v1/auth/ { add_header Cache-Control no-store always; return 401 '{}'; }
    location /api/failure { return 503 '{}'; }
    location / { return 200 '{}'; }
}
server { listen 8000; default_type application/json; location / { return 200 '{}'; } }
"""
        gateway = (gateway.replace("http://api:8080", "http://127.0.0.1:8080")
                   .replace("http://ml-analyzer:8000", "http://127.0.0.1:8000")
                   .replace("http://web:80", "http://127.0.0.1:3000"))
        host = host.replace("/etc/letsencrypt/live/example.com", "/fixtures")
        for name, entrypoint in (("gateway", gateway), ("host", host)):
            config = "events {}\nhttp { include /etc/nginx/mime.types;\n" + entrypoint + web + mocks + "\n}\n"
            (fixtures / "nginx.conf").write_text(config)
            mount = f"{fixtures}:/fixtures:ro"
            print(f"{name}: {run('docker', 'run', '--rm', '-v', mount, IMAGE, 'nginx', '-t', '-c', '/fixtures/nginx.conf')}")
            container = run("docker", "run", "--rm", "-d", "-v", mount,
                            "-p", "127.0.0.1::80", "-p", "127.0.0.1::443", "-p", "127.0.0.1::3000",
                            IMAGE, "nginx", "-c", "/fixtures/nginx.conf", "-g", "daemon off;")
            try:
                port = 80 if name == "gateway" else 443
                address = run("docker", "port", container, f"{port}/tcp")
                base = ("http://" if port == 80 else "https://") + address
                static = "http://" + run("docker", "port", container, "3000/tcp")
                for origin in (base, static):
                    for path, status in (("/", 200), ("/index.html", 200), ("/signup", 200), ("/bundle.js", 200), ("/missing.js", 404)):
                        headers = check(origin + path, status, policy)
                        if path == "/index.html":
                            assert headers.get("cache-control") == ["no-cache"]
                        if path == "/bundle.js":
                            assert headers.get("cache-control") == ["public, max-age=31536000, immutable"]
                for path, status in (("/healthz", 200), ("/ml/healthz", 200), ("/api/failure", 503), ("/api/v1/auth/me", 401)):
                    headers = check(base + path, status, policy)
                    if path == "/api/v1/auth/me":
                        assert headers.get("cache-control") == ["no-store"]
                check(base + "/api/v1/auth/signup", 413, policy, "-D", "-", "-o", "/dev/null", "--data-binary", "@" + str(fixtures / "oversized"))
                if name == "host":
                    redirect = "http://" + run("docker", "port", container, "80/tcp")
                    check(redirect + "/", 301, policy)
                print(f"{name}: curl -I assertions passed (SPA, assets, 401/404/503, ML, cache headers); 413 headers passed")
            finally:
                run("docker", "stop", container)


if __name__ == "__main__":
    main()
