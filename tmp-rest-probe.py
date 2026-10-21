import json, urllib.request, urllib.error

HUB = "http://1.13.158.180:8444"
ADMIN = "4380b93e2cb636f200cbcd758924ed62"


def req(method, path, token=None, body=None, headers=None):
    r = urllib.request.Request(HUB + path, method=method)
    if token:
        r.add_header("Authorization", "Bearer " + token)
    if headers:
        for k, v in headers.items():
            r.add_header(k, v)
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        r.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(r, data=data, timeout=15) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()


if __name__ == "__main__":
    c, h, b = req("GET", "/api/v1/nodes")
    print("no-auth:", c, b[:120])
    c, h, b = req("GET", "/api/v1/nodes", token=ADMIN)
    print("admin-auth:", c)
    print(b.decode()[:800])
