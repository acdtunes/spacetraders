#!/usr/bin/env python3
"""Captain-loop dashboard: real-time view of the autonomous fleet.

Single file, stdlib only. Reads the live artifacts (Postgres via docker exec,
captain workspace files, supervisor log) and serves one auto-refreshing page.

    python3 dashboard/captain_dashboard.py   ->  http://localhost:8899
"""
import json, os, re, subprocess, time
from http.server import HTTPServer, BaseHTTPRequestHandler

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CAPTAIN = os.path.join(ROOT, "captain")
GOBOT = os.path.join(ROOT, "gobot")
PSQL = ["docker", "exec", "spacetraders-postgres", "psql", "-U", "spacetraders",
        "-d", "spacetraders", "-t", "-A", "-F", "\t", "-c"]

_cache = {}

def sql(q, ttl=4):
    key = ("sql", q)
    hit = _cache.get(key)
    if hit and time.time() - hit[0] < ttl:
        return hit[1]
    try:
        out = subprocess.run(PSQL + [q], capture_output=True, text=True, timeout=6).stdout
        rows = [line.split("\t") for line in out.strip().split("\n") if line.strip()]
    except Exception:
        rows = []
    _cache[key] = (time.time(), rows)
    return rows

def read(path, tail=None):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            data = f.read()
        return data[-tail:] if tail else data
    except Exception:
        return ""

def gate_status(ttl=60):
    hit = _cache.get("gate")
    if hit and time.time() - hit[0] < ttl:
        return hit[1]
    try:
        out = subprocess.run([os.path.join(GOBOT, "bin", "spacetraders"), "construction",
                              "status", "X1-PZ28-I67", "--player-id", "1"],
                             capture_output=True, text=True, timeout=8, cwd=GOBOT).stdout
    except Exception:
        out = ""
    mats = re.findall(r"- (\w+): (\d+)/(\d+)", out)
    prog = re.search(r"Progress: ([\d.]+)%", out)
    result = {"progress": float(prog.group(1)) if prog else None,
              "materials": [{"name": m[0], "have": int(m[1]), "need": int(m[2])} for m in mats]}
    _cache["gate"] = (time.time(), result)
    return result

def collect():
    treasury = sql("SELECT balance_after FROM transactions ORDER BY timestamp DESC, created_at DESC, id DESC LIMIT 1")
    day = sql("SELECT balance_before FROM transactions WHERE timestamp >= now() - interval '24 hours' ORDER BY timestamp ASC LIMIT 1")
    spark = sql("SELECT extract(epoch from timestamp), balance_after FROM transactions "
                "WHERE timestamp >= now() - interval '6 hours' ORDER BY timestamp ASC", ttl=20)
    containers = sql("SELECT id, container_type, status, to_char(started_at,'HH24:MI') FROM containers "
                     "WHERE status IN ('RUNNING','STOPPING') ORDER BY started_at")
    events = sql("SELECT count(*) FROM captain_events WHERE processed_at IS NULL")
    recent_events = sql("SELECT id, type, ship, to_char(created_at,'HH24:MI:SS'), processed_at IS NOT NULL "
                        "FROM captain_events ORDER BY id DESC LIMIT 8")
    ships = sql("SELECT ship_symbol, nav_status, location_symbol, fuel_current, fuel_capacity, "
                "cargo_units, cargo_capacity FROM ships ORDER BY ship_symbol")

    log = read(os.path.join(CAPTAIN, "state", "captain-log.md"))
    headers = re.findall(r"^## (.+)$", log, re.M)
    last_entry = log[log.rfind("\n## "):][:4000] if "## " in log else ""

    decisions, open_count = {}, 0
    for line in read(os.path.join(CAPTAIN, "state", "decisions.jsonl")).splitlines():
        try:
            d = json.loads(line); decisions[d.get("id")] = d
        except Exception:
            pass
    open_d = [d for d in decisions.values() if not d.get("outcome")]

    reports = []
    bugdir = os.path.join(CAPTAIN, "reports", "bugs")
    if os.path.isdir(bugdir):
        for f in sorted(os.listdir(bugdir)):
            if f.endswith(".md"):
                head = read(os.path.join(bugdir, f))[:400]
                st = re.search(r"^status: (\S+)", head, re.M)
                kd = re.search(r"^kind: (\S+)", head, re.M)
                reports.append({"name": f[:-3], "status": st.group(1) if st else "?",
                                "kind": kd.group(1) if kd else "fix"})

    t = int(treasury[0][0]) if treasury else 0
    base = int(day[0][0]) if day else t
    return {
        "ts": time.strftime("%H:%M:%S"),
        "treasury": t,
        "rate": round((t - base) / 24),
        "spark": [[float(r[0]), int(r[1])] for r in spark if len(r) == 2],
        "queue": int(events[0][0]) if events else 0,
        "session": headers[-1] if headers else "",
        "sessions_total": len(headers),
        "open_decisions": len(open_d),
        "containers": [{"id": c[0], "type": c[1], "status": c[2], "since": c[3]} for c in containers if len(c) >= 4],
        "events": [{"id": e[0], "type": e[1], "ship": e[2], "at": e[3], "done": e[4] == "t"} for e in recent_events if len(e) >= 5],
        "ships": [{"sym": s[0], "nav": s[1], "loc": s[2], "fuel": f"{s[3]}/{s[4]}", "cargo": f"{s[5]}/{s[6]}"} for s in ships if len(s) >= 7],
        "reports": reports[::-1][:8],
        "gate": gate_status(),
        "last_entry": last_entry,
        "supervisor": read(os.path.join(GOBOT, "captain-supervisor.log"), tail=1200),
        "captain_alive": subprocess.run(["pgrep", "-f", "bin/captain"], capture_output=True).returncode == 0,
    }

PAGE = """<!DOCTYPE html><html><head><meta charset="utf-8"><title>Captain Loop</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root{--bg:#0F1420;--card:#161D2E;--ink:#E6E9EF;--muted:#98A1B3;--line:#2A3550;
--accent:#7DB1FF;--good:#3DD68C;--warn:#F5C518;--bad:#FF6369}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);
font:14px/1.45 "SF Mono",Menlo,monospace;padding:16px}
h1{font-size:16px;margin:0 0 12px;color:var(--muted);font-weight:600}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:12px}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px;margin-bottom:12px}
.tile{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px 14px}
.tile .v{font-size:24px;font-weight:700;margin-top:2px}.tile .l{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.06em}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:14px;overflow:auto}
.card h2{font-size:12px;color:var(--muted);margin:0 0 8px;text-transform:uppercase;letter-spacing:.06em}
table{width:100%;border-collapse:collapse}td,th{padding:3px 8px 3px 0;text-align:left;font-size:12.5px}
th{color:var(--muted);font-weight:400}
.dot{display:inline-block;width:8px;height:8px;border-radius:50%;margin-right:6px;vertical-align:baseline}
.RUNNING{background:var(--good)}.STOPPING{background:var(--warn)}
.st-new{color:var(--warn)}.st-in_progress{color:var(--accent)}.st-merged{color:var(--good)}
.st-gate_failed,.st-awaiting_human{color:var(--bad)}
pre{white-space:pre-wrap;font-size:11.5px;color:var(--muted);margin:0;max-height:340px;overflow:auto}
.entry{max-height:420px}.entry b{color:var(--ink)}
#spark{width:100%;height:60px}.bar{background:var(--line);border-radius:4px;height:10px;overflow:hidden}
.bar>div{background:var(--good);height:100%;border-radius:4px 0 0 4px}
.ok{color:var(--good)}.down{color:var(--bad)}
small{color:var(--muted)}
</style></head><body>
<h1>CAPTAIN LOOP <span id="alive"></span> <small id="ts" style="float:right"></small></h1>
<div class="tiles">
 <div class="tile"><div class="l">Treasury</div><div class="v" id="treasury">–</div><svg id="spark" preserveAspectRatio="none"></svg></div>
 <div class="tile"><div class="l">Rate /hr (24h)</div><div class="v" id="rate">–</div></div>
 <div class="tile"><div class="l">Sessions</div><div class="v" id="sessions">–</div></div>
 <div class="tile"><div class="l">Event queue</div><div class="v" id="queue">–</div></div>
 <div class="tile"><div class="l">Open decisions</div><div class="v" id="odec">–</div></div>
 <div class="tile"><div class="l">Jump gate</div><div class="v" id="gatep">–</div><div class="bar"><div id="gatebar" style="width:0%"></div></div><small id="gatemats"></small></div>
</div>
<div class="grid">
 <div class="card"><h2>Containers</h2><table id="containers"></table></div>
 <div class="card"><h2>Fleet</h2><table id="ships"></table></div>
 <div class="card"><h2>Recent events</h2><table id="events"></table></div>
 <div class="card"><h2>Fix pipeline</h2><table id="reports"></table></div>
 <div class="card" style="grid-column:1/-1"><h2>Latest log entry — <span id="sesshead"></span></h2><pre class="entry" id="entry"></pre></div>
 <div class="card" style="grid-column:1/-1"><h2>Supervisor</h2><pre id="sup"></pre></div>
</div>
<script>
const fmt=n=>n.toLocaleString();
async function tick(){
 try{
  const d=await (await fetch('/data.json')).json();
  ts.textContent=d.ts; treasury.textContent=fmt(d.treasury);
  rate.textContent=(d.rate>=0?'+':'')+fmt(d.rate);
  sessions.textContent=d.sessions_total; queue.textContent=d.queue; odec.textContent=d.open_decisions;
  alive.innerHTML=d.captain_alive?'<span class="ok">● supervisor up</span>':'<span class="down">● supervisor DOWN</span>';
  if(d.gate&&d.gate.progress!=null){gatep.textContent=d.gate.progress+'%';gatebar.style.width=Math.max(d.gate.progress,1)+'%';
   gatemats.textContent=d.gate.materials.map(m=>`${m.name} ${m.have}/${m.need}`).join('  ');}
  // sparkline: single series, thin line, no legend (title names it)
  if(d.spark.length>1){const xs=d.spark.map(p=>p[0]),ys=d.spark.map(p=>p[1]);
   const x0=Math.min(...xs),x1=Math.max(...xs),y0=Math.min(...ys),y1=Math.max(...ys)||1;
   const pts=d.spark.map(p=>`${(300*(p[0]-x0)/(x1-x0||1)).toFixed(1)},${(56-52*(p[1]-y0)/(y1-y0||1)+2).toFixed(1)}`).join(' ');
   spark.setAttribute('viewBox','0 0 300 60');
   spark.innerHTML=`<polyline points="${pts}" fill="none" stroke="#7DB1FF" stroke-width="2"/>`;}
  containers.innerHTML='<tr><th>container</th><th>type</th><th>state</th><th>since</th></tr>'+
   d.containers.map(c=>`<tr><td>${c.id.slice(0,42)}</td><td>${c.type}</td><td><span class="dot ${c.status}"></span>${c.status}</td><td>${c.since}</td></tr>`).join('');
  ships.innerHTML='<tr><th>ship</th><th>nav</th><th>loc</th><th>fuel</th><th>cargo</th></tr>'+
   d.ships.map(s=>`<tr><td>${s.sym}</td><td>${s.nav}</td><td>${s.loc}</td><td>${s.fuel}</td><td>${s.cargo}</td></tr>`).join('');
  events.innerHTML='<tr><th>#</th><th>type</th><th>ship</th><th>at</th><th>state</th></tr>'+
   d.events.map(e=>`<tr><td>${e.id}</td><td>${e.type}</td><td>${e.ship}</td><td>${e.at}</td><td>${e.done?'processed':'<b class="st-new">queued</b>'}</td></tr>`).join('');
  reports.innerHTML='<tr><th>report</th><th>kind</th><th>status</th></tr>'+
   d.reports.map(r=>`<tr><td>${r.name.slice(0,48)}</td><td>${r.kind}</td><td class="st-${r.status}">${r.status}</td></tr>`).join('');
  sesshead.textContent=d.session; entry.textContent=d.last_entry; sup.textContent=d.supervisor;
 }catch(e){alive.innerHTML='<span class="down">● dashboard fetch failed</span>';}
}
tick(); setInterval(tick,5000);
</script></body></html>"""

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        if self.path == "/data.json":
            body = json.dumps(collect()).encode()
            ctype = "application/json"
        else:
            body = PAGE.encode(); ctype = "text/html; charset=utf-8"
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

if __name__ == "__main__":
    print("captain dashboard: http://localhost:8899")
    HTTPServer(("127.0.0.1", 8899), H).serve_forever()
