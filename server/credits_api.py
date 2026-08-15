#!/usr/bin/env python3
# credits_api.py — TRAE SOLO 积分监控小服务（端口 7865）
# GET /accounts → 调 credit_bin -json（缓存 60s）返回各账号积分/使用情况
# GET /healthz  → 健康检查
# 刷新后把各账号剩余积分推送给 traework2api（POST /internal/credits），
# 供 pool 用真实剩余积分挑选账号。
import json, os, subprocess, threading, time, http.server, urllib.request, urllib.error

CREDIT_BIN = "/opt/traework2api/credit_bin"
CACHE_TTL = 60
PORT = 7865
POOL_URL = "http://127.0.0.1:7864/internal/credits"
_lock = threading.Lock()
_cache = {"ts": 0.0, "data": None}

def _api_key():
    """从 .env 读 TW2A_API_KEY（pool 鉴权用）。"""
    try:
        with open("/opt/traework2api/.env") as f:
            for line in f:
                line = line.strip()
                if line.startswith("TW2A_API_KEY="):
                    return line.split("=", 1)[1].strip().strip('"').strip("'")
    except Exception:
        pass
    return os.environ.get("TW2A_API_KEY", "")

def push_credits(data):
    """把 credit_bin 返回的剩余积分推送给 pool。"""
    accounts = (data or {}).get("accounts") or []
    if not accounts:
        return
    payload = {"accounts": [{"uid": a.get("uid", ""), "remain": a.get("remain", 0)} for a in accounts]}
    key = _api_key()
    req = urllib.request.Request(POOL_URL, data=json.dumps(payload).encode(),
                                 method="POST", headers={"Content-Type": "application/json"})
    if key:
        req.add_header("Authorization", "Bearer " + key)
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            resp.read()
    except Exception:
        pass  # 推送失败不阻塞积分服务

def fetch():
    now = time.time()
    with _lock:
        if _cache["data"] is not None and now - _cache["ts"] < CACHE_TTL:
            return _cache["data"]
        try:
            out = subprocess.run([CREDIT_BIN, "-json"], capture_output=True, text=True,
                                 timeout=90, cwd="/opt/traework2api")
            data = json.loads(out.stdout) if out.returncode == 0 else {"error": out.stderr[:300]}
        except Exception as e:
            data = {"error": str(e)}
        _cache["ts"] = now
        _cache["data"] = data
        # 刷新成功后推送剩余积分给 pool
        if "error" not in data:
            push_credits(data)
        return data

class Handler(http.server.BaseHTTPRequestHandler):
    def _send(self, code, obj):
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path.split("?")[0]
        if path in ("/healthz", "/"):
            self._send(200, {"ok": True, "service": "trae-solo-credits"})
        elif path == "/accounts":
            self._send(200, fetch())
        else:
            self._send(404, {"error": "not found"})

    def log_message(self, *args):
        pass

if __name__ == "__main__":
    server = http.server.ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(f"credits_api listening on :{PORT}", flush=True)
    server.serve_forever()
