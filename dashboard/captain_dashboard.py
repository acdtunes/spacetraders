#!/usr/bin/env python3
"""Captain-loop mission console. Single file, stdlib only.  :8899"""
import json, os, re, subprocess, time
from http.server import HTTPServer, BaseHTTPRequestHandler

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CAPTAIN, GOBOT = os.path.join(ROOT, "captain"), os.path.join(ROOT, "gobot")
PSQL = ["docker", "exec", "spacetraders-postgres", "psql", "-U", "spacetraders",
        "-d", "spacetraders", "-t", "-A", "-F", "\t", "-c"]
_cache = {}

def sql(q, ttl=4):
    hit = _cache.get(q)
    if hit and time.time() - hit[0] < ttl: return hit[1]
    try:
        out = subprocess.run(PSQL + [q], capture_output=True, text=True, timeout=6).stdout
        rows = [l.split("\t") for l in out.strip().split("\n") if l.strip()]
    except Exception: rows = []
    _cache[q] = (time.time(), rows); return rows

def read(path, tail=None):
    try:
        with open(path, encoding="utf-8", errors="replace") as f: d = f.read()
        return d[-tail:] if tail else d
    except Exception: return ""

def gate(ttl=60):
    hit = _cache.get("gate")
    if hit and time.time() - hit[0] < ttl: return hit[1]
    try:
        out = subprocess.run([os.path.join(GOBOT, "bin", "spacetraders"), "construction",
                              "status", "X1-PZ28-I67", "--player-id", "1"],
                             capture_output=True, text=True, timeout=8, cwd=GOBOT).stdout
    except Exception: out = ""
    mats = re.findall(r"- (\w+): (\d+)/(\d+)", out)
    prog = re.search(r"Progress: ([\d.]+)%", out)
    r = {"progress": float(prog.group(1)) if prog else None,
         "materials": [{"name": m[0], "have": int(m[1]), "need": int(m[2])} for m in mats if m[2] != "1"]}
    _cache["gate"] = (time.time(), r); return r

def collect():
    t_row = sql("SELECT balance_after FROM transactions ORDER BY timestamp DESC, created_at DESC, id DESC LIMIT 1")
    base = sql("SELECT balance_before FROM transactions WHERE timestamp >= now() - interval '24 hours' ORDER BY timestamp ASC LIMIT 1")
    spark = sql("SELECT extract(epoch from timestamp), balance_after FROM transactions WHERE timestamp >= now() - interval '24 hours' ORDER BY timestamp ASC", 20)
    hourly = sql("SELECT to_char(date_trunc('hour', timestamp),'HH24:00'), sum(amount) FROM transactions WHERE timestamp >= now() - interval '24 hours' GROUP BY 1 ORDER BY 1", 30)
    containers = sql("SELECT id, container_type, status, to_char(started_at,'HH24:MI'), extract(epoch from (now()-started_at)) FROM containers WHERE status IN ('RUNNING','STOPPING') ORDER BY started_at")
    events = sql("SELECT count(*) FROM captain_events WHERE processed_at IS NULL")
    recent = sql("SELECT id, type, ship, to_char(created_at,'HH24:MI:SS'), processed_at IS NOT NULL FROM captain_events ORDER BY id DESC LIMIT 10")
    ships = sql("SELECT ship_symbol, nav_status, location_symbol, fuel_current, fuel_capacity, cargo_units, cargo_capacity FROM ships ORDER BY ship_symbol")
    log = read(os.path.join(CAPTAIN, "state", "captain-log.md"))
    heads = re.findall(r"^## (.+)$", log, re.M)
    last = log[log.rfind("\n## "):][:5000] if "## " in log else ""
    open_d = {}
    for line in read(os.path.join(CAPTAIN, "state", "decisions.jsonl")).splitlines():
        try:
            d = json.loads(line); open_d[d.get("id")] = d
        except Exception: pass
    reports = []
    bugdir = os.path.join(CAPTAIN, "reports", "bugs")
    for d, closed in ((bugdir, False), (os.path.join(bugdir, "closed"), True)):
        if not os.path.isdir(d):
            continue
        for f in os.listdir(d):
            if f.endswith(".md"):
                fp = os.path.join(d, f)
                h = read(fp)[:400]
                st = re.search(r"^status: (\S+)", h, re.M); kd = re.search(r"^kind: (\S+)", h, re.M)
                reports.append({"name": f[:-3], "status": st.group(1) if st else "?",
                                "kind": kd.group(1) if kd else "fix", "closed": closed,
                                "mtime": os.path.getmtime(fp)})
    # Active first, then closed; within each group most recent activity first.
    reports.sort(key=lambda r: (r["closed"], -r["mtime"]))
    t = int(t_row[0][0]) if t_row else 0
    b = int(base[0][0]) if base else t
    return {"ts": time.strftime("%H:%M:%S"), "treasury": t, "delta24": t - b, "rate": round((t - b) / 24),
            "spark": [[float(r[0]), int(r[1])] for r in spark if len(r) == 2],
            "hourly": [[r[0], int(r[1])] for r in hourly if len(r) == 2],
            "queue": int(events[0][0]) if events else 0,
            "session": heads[-1] if heads else "", "heads": heads[-6:][::-1], "sessions_total": len(heads),
            "open_decisions": sum(1 for d in open_d.values() if not d.get("outcome")),
            "containers": [{"id": c[0], "type": c[1], "status": c[2], "since": c[3], "up": int(float(c[4]))} for c in containers if len(c) >= 5],
            "events": [{"id": e[0], "type": e[1], "ship": e[2], "at": e[3], "done": e[4] == "t"} for e in recent if len(e) >= 5],
            "ships": [{"sym": s[0], "nav": s[1], "loc": s[2], "f": int(s[3]), "fc": int(s[4]), "c": int(s[5]), "cc": int(s[6])} for s in ships if len(s) >= 7],
            "reports": reports[:8], "gate": gate(), "last_entry": last,
            "supervisor": read(os.path.join(GOBOT, "captain-supervisor.log"), 1600),
            "alive": subprocess.run(["pgrep", "-f", "bin/captain"], capture_output=True).returncode == 0,
            "session_state": session_state(), "tokens": token_stats()}

def session_state():
    """Is a captain session running now? Else, ETA to the next one."""
    pid = subprocess.run(["pgrep", "-f", "claude -p"], capture_output=True, text=True).stdout.split()
    if pid:
        try:
            secs = int(subprocess.run(["ps", "-o", "etime=", "-p", pid[0]],
                                      capture_output=True, text=True).stdout.strip()
                       .replace("-", ":").split(":")[-2]) * 60
        except Exception:
            secs = 0
        return {"active": True, "elapsed": secs}
    try:
        last = os.path.getmtime(os.path.join(CAPTAIN, "state", "captain-log.md"))
    except Exception:
        last = time.time()
    return {"active": False, "eta": max(0, int(last + 45 * 60 - time.time()))}

_tok_files = {}

def token_stats(ttl=30):
    """Sum token usage across captain session transcripts (strategy sessions in
    the captain workspace + fix sessions in worktrees). Incremental: files are
    re-parsed only when (mtime,size) changes — i.e. the live session's file."""
    hit = _cache.get("tok")
    if hit and time.time() - hit[0] < ttl:
        return hit[1]
    import glob
    home = os.path.expanduser("~/.claude/projects")
    paths = glob.glob(os.path.join(home, "*spacetraders-captain*", "*.jsonl")) +             glob.glob(os.path.join(home, "*captain-worktrees*", "*.jsonl"))
    total = {"in": 0, "out": 0, "cache": 0}
    newest, newest_mtime = None, 0
    for f in paths:
        try:
            st = os.stat(f)
        except OSError:
            continue
        key = (st.st_mtime, st.st_size)
        cached = _tok_files.get(f)
        if not cached or cached[0] != key:
            sums = {"in": 0, "out": 0, "cache": 0}
            try:
                with open(f, encoding="utf-8", errors="replace") as fh:
                    for line in fh:
                        if '"usage"' not in line:
                            continue
                        try:
                            u = json.loads(line).get("message", {}).get("usage")
                        except Exception:
                            continue
                        if u:
                            sums["in"] += u.get("input_tokens", 0) + u.get("cache_creation_input_tokens", 0)
                            sums["out"] += u.get("output_tokens", 0)
                            sums["cache"] += u.get("cache_read_input_tokens", 0)
            except OSError:
                continue
            _tok_files[f] = (key, sums)
        sums = _tok_files[f][1]
        for k in total:
            total[k] += sums[k]
        if st.st_mtime > newest_mtime:
            newest, newest_mtime = f, st.st_mtime
    cur = _tok_files.get(newest, (None, {"in": 0, "out": 0, "cache": 0}))[1] if newest else {"in": 0, "out": 0, "cache": 0}
    r = {"session": cur, "total": total, "files": len(paths)}
    _cache["tok"] = (time.time(), r)
    return r

PAGE = r"""<!DOCTYPE html><html><head><meta charset="utf-8"><title>TORWIND // Captain Loop</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root{--bg0:#080B12;--bg1:#0D1220;--card:rgba(22,29,46,.72);--edge:rgba(125,177,255,.14);
--ink:#EAEEF6;--mut:#8B95AB;--dim:#5A6478;--acc:#7DB1FF;--good:#3DD68C;--warn:#F5C518;--bad:#FF6369;
--mono:"SF Mono",ui-monospace,Menlo,monospace;--sans:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
*{box-sizing:border-box;margin:0}
body{background:radial-gradient(1200px 800px at 75% -10%,#141d38 0%,var(--bg1) 45%,var(--bg0) 100%) fixed;
color:var(--ink);font:14px/1.5 var(--sans);padding:20px;min-height:100vh}
header{display:flex;align-items:baseline;gap:14px;margin-bottom:18px;flex-wrap:wrap}
header h1{font:700 15px var(--mono);letter-spacing:.18em;color:var(--mut)}
header h1 b{color:var(--ink)}
.pill{font:600 11px var(--mono);padding:3px 10px;border-radius:999px;border:1px solid var(--edge)}
.pill.ok{color:var(--good);border-color:rgba(61,214,140,.35)}
.pill.down{color:var(--bad);border-color:rgba(255,99,105,.4)}
#clock{margin-left:auto;font:12px var(--mono);color:var(--dim)}
.grid{display:grid;grid-template-columns:repeat(12,1fr);gap:14px}
.card{background:var(--card);backdrop-filter:blur(10px);border:1px solid var(--edge);border-radius:14px;
padding:16px;overflow:hidden;transition:border-color .25s}
.card:hover{border-color:rgba(125,177,255,.3)}
.card h2{font:600 10.5px var(--mono);letter-spacing:.14em;text-transform:uppercase;color:var(--dim);margin-bottom:10px}
.hero{grid-column:span 5;display:flex;flex-direction:column}
.hero .big{font:700 40px/1.05 var(--mono);font-variant-numeric:tabular-nums;letter-spacing:-.01em}
.hero .sub{color:var(--mut);font:12px var(--mono);margin-top:4px}
.hero .sub b{color:var(--good)}
#sparkwrap{position:relative;flex:1;min-height:120px;margin-top:12px}
#spark{width:100%;height:100%;display:block}
#xhair{position:absolute;top:0;bottom:0;width:1px;background:rgba(125,177,255,.35);display:none;pointer-events:none}
#tip{position:absolute;background:#1B2437;border:1px solid var(--edge);border-radius:8px;padding:6px 9px;
font:11px var(--mono);pointer-events:none;display:none;white-space:nowrap;z-index:5}
.kpis{grid-column:span 7;display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:14px}
.kpi{background:var(--card);border:1px solid var(--edge);border-radius:14px;padding:14px 16px}
.kpi .l{font:600 10px var(--mono);letter-spacing:.12em;text-transform:uppercase;color:var(--dim)}
.kpi .v{font:700 26px var(--mono);font-variant-numeric:tabular-nums;margin-top:4px}
.kpi .h{font:10.5px var(--mono);color:var(--mut);margin-top:4px;min-height:13px}
.kpi.gate{grid-column:1/-1}
.matrow{display:flex;align-items:center;gap:10px;margin-top:8px;font:11px var(--mono);color:var(--mut)}
.matrow .name{width:170px;color:var(--ink)}
.bar{flex:1;background:rgba(125,177,255,.10);border-radius:99px;height:8px;overflow:hidden}
.bar>i{display:block;background:linear-gradient(90deg,var(--acc),#9CC5FF);height:100%;border-radius:99px;
transition:width .6s cubic-bezier(.2,.8,.2,1)}
.span4{grid-column:span 4}.span6{grid-column:span 6}.span8{grid-column:span 8}.span12{grid-column:span 12}
table{width:100%;border-collapse:collapse;font:12.5px var(--mono)}
th{color:var(--dim);font-weight:500;text-align:left;padding:0 10px 6px 0;font-size:10.5px;letter-spacing:.08em;text-transform:uppercase}
td{padding:5px 10px 5px 0;border-top:1px solid rgba(125,177,255,.06);font-variant-numeric:tabular-nums;vertical-align:middle}
.dot{display:inline-block;width:7px;height:7px;border-radius:50%;margin-right:7px}
.d-RUNNING{background:var(--good);box-shadow:0 0 8px rgba(61,214,140,.6)}
.d-STOPPING{background:var(--warn)}
.tag{font:600 10px var(--mono);padding:2px 8px;border-radius:6px;background:rgba(125,177,255,.12);color:var(--acc)}
.tag.mfg{background:rgba(200,140,255,.14);color:#C79BFF}
.tag.scout{background:rgba(61,214,140,.12);color:var(--good)}
.tag.constr{background:rgba(245,197,24,.13);color:var(--warn)}
.st{font:600 11px var(--mono)}
.st-new{color:var(--warn)}.st-in_progress{color:var(--acc)}.st-merged{color:var(--good)}
.st-gate_failed,.st-awaiting_human{color:var(--bad)}.st-obsolete,.st-rejected,.st-resolved{color:var(--dim)}
.meter{display:inline-block;width:56px;height:5px;background:rgba(125,177,255,.12);border-radius:99px;overflow:hidden;margin-right:6px;vertical-align:2px}
.meter>i{display:block;height:100%;border-radius:99px;background:var(--acc);transition:width .5s}
.meter.low>i{background:var(--bad)}
.qd{color:var(--warn);font-weight:700}
pre{font:11.5px/1.6 var(--mono);color:var(--mut);white-space:pre-wrap;max-height:360px;overflow:auto}
pre::-webkit-scrollbar{width:8px}pre::-webkit-scrollbar-thumb{background:var(--edge);border-radius:8px}
.entry{max-height:460px;color:#C4CBDA}
.entry .hd{color:var(--ink);font-weight:700}
.entry .em{color:var(--acc)}
.heads{list-style:none;font:11.5px var(--mono);color:var(--mut)}
.heads li{padding:4px 0;border-top:1px solid rgba(125,177,255,.06);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.heads li:first-child{color:var(--ink)}
#bars{display:flex;align-items:flex-end;gap:2px;height:64px;margin-top:8px}
#bars .b{flex:1;border-radius:4px 4px 0 0;background:var(--acc);min-height:2px;position:relative;transition:height .5s}
#bars .b.neg{background:var(--bad)}
#bars .b:hover::after{content:attr(data-t);position:absolute;bottom:calc(100% + 6px);left:50%;transform:translateX(-50%);
background:#1B2437;border:1px solid var(--edge);border-radius:6px;padding:3px 7px;font:10px var(--mono);color:var(--ink);white-space:nowrap;z-index:4}
.flash{animation:fl .8s}
@keyframes fl{0%{color:var(--good)}100%{color:inherit}}
.sup .err{color:var(--bad)}.sup .ok{color:var(--good)}
#modal{position:fixed;inset:0;background:rgba(5,8,15,.75);backdrop-filter:blur(4px);display:none;
align-items:flex-start;justify-content:center;padding:6vh 16px;z-index:50}
#modal.open{display:flex}
#mbox{background:#131A2B;border:1px solid var(--edge);border-radius:14px;max-width:860px;width:100%;
max-height:84vh;display:flex;flex-direction:column;box-shadow:0 24px 80px rgba(0,0,0,.5)}
#mhead{display:flex;align-items:center;gap:10px;padding:14px 18px;border-bottom:1px solid var(--edge)}
#mtitle{font:600 13px var(--mono);color:var(--ink);flex:1;word-break:break-all}
#mclose{background:none;border:1px solid var(--edge);border-radius:8px;color:var(--mut);
font:14px var(--mono);padding:2px 10px;cursor:pointer}
#mclose:hover{color:var(--ink);border-color:var(--acc)}
#mbody{padding:16px 18px;overflow:auto;font:12px/1.65 var(--mono);color:#C4CBDA;white-space:pre-wrap}
#mbody .hd{color:var(--ink);font-weight:700}#mbody .em{color:var(--acc)}
tr.clickable{cursor:pointer}tr.clickable:hover td{background:rgba(125,177,255,.05)}
.closedrow td{opacity:.55}
@media(max-width:1100px){.hero,.kpis{grid-column:span 12}.span4,.span6,.span8{grid-column:span 12}}
@media(prefers-reduced-motion:reduce){*{transition:none!important;animation:none!important}}
</style></head><body>
<header>
 <h1>⚓ <b>TORWIND</b> // CAPTAIN LOOP</h1>
 <span class="pill" id="alive">…</span>
 <span class="pill" id="sesspill">…</span>
 <span class="pill" id="sessact">…</span>
 <span id="clock"></span>
</header>
<div class="grid">
 <div class="card hero">
  <h2>Treasury</h2>
  <div class="big" id="treasury">–</div>
  <div class="sub"><b id="delta">–</b> / 24h &nbsp;·&nbsp; <span id="rate">–</span>/hr</div>
  <div id="sparkwrap"><svg id="spark" preserveAspectRatio="none"></svg><div id="xhair"></div><div id="tip"></div></div>
 </div>
 <div class="kpis">
  <div class="kpi"><div class="l">Rate / hr</div><div class="v" id="ratek">–</div><div class="h" id="rateh"></div></div>
  <div class="kpi"><div class="l">Sessions</div><div class="v" id="sessions">–</div><div class="h">log entries</div></div>
  <div class="kpi"><div class="l">Event queue</div><div class="v" id="queue">–</div><div class="h" id="queueh"></div></div>
  <div class="kpi"><div class="l">Open decisions</div><div class="v" id="odec">–</div><div class="h">awaiting review</div></div>
  <div class="kpi"><div class="l">Tokens · session</div><div class="v" id="tokv">–</div><div class="h" id="tokh"></div></div>
  <div class="kpi gate"><div class="l">Jump gate · X1-PZ28-I67</div><div class="v" id="gatep">–</div><div id="gatemats"></div></div>
 </div>
 <div class="card span6"><h2>Net flow / hour · 24h</h2><div id="bars"></div></div>
 <div class="card span6"><h2>Recent sessions</h2><ul class="heads" id="heads"></ul></div>
 <div class="card span6"><h2>Containers</h2><table id="containers"></table></div>
 <div class="card span6"><h2>Fleet</h2><table id="ships"></table></div>
 <div class="card span6"><h2>Event stream</h2><table id="events"></table></div>
 <div class="card span6"><h2>Fix pipeline</h2><table id="reports"></table></div>
 <div class="card span12"><h2 id="sesshead">Latest log entry</h2><pre class="entry" id="entry"></pre></div>
 <div class="card span12"><h2>Supervisor</h2><pre class="sup" id="sup"></pre></div>
</div>
<div id="modal"><div id="mbox"><div id="mhead"><span id="mtitle"></span><button id="mclose">esc</button></div><pre id="mbody"></pre></div></div>
<script>
const $=id=>document.getElementById(id),fmt=n=>n.toLocaleString();
const TYPETAG=t=>/MANUF|mfg/i.test(t)?'mfg':/SCOUT/i.test(t)?'scout':/CONSTR/i.test(t)?'constr':'';
const up=s=>s>=3600?Math.floor(s/3600)+'h'+Math.floor(s%3600/60)+'m':Math.floor(s/60)+'m';
let lastT=null,SP=[];
function setNum(el,val,txt){if(el.dataset.v!==String(val)){el.classList.remove('flash');void el.offsetWidth;el.classList.add('flash');}el.dataset.v=val;el.textContent=txt;}
function drawSpark(d){SP=d;const svg=$('spark');
 if(d.length<2){svg.innerHTML='<text x="8" y="24" fill="#5A6478" font-size="12" font-family="monospace">no transactions in window — income stalled?</text>';svg.setAttribute('viewBox','0 0 300 60');return}
 const W=600,H=140,xs=d.map(p=>p[0]),ys=d.map(p=>p[1]);
 const x0=Math.min(...xs),x1=Math.max(...xs),y0=Math.min(...ys),y1=Math.max(...ys);
 const X=t=>W*(t-x0)/((x1-x0)||1),Y=v=>H-8-(H-20)*(v-y0)/((y1-y0)||1);
 const pts=d.map(p=>X(p[0]).toFixed(1)+','+Y(p[1]).toFixed(1)).join(' ');
 svg.setAttribute('viewBox',`0 0 ${W} ${H}`);
 svg.innerHTML=`<defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1">
  <stop offset="0" stop-color="#7DB1FF" stop-opacity=".28"/><stop offset="1" stop-color="#7DB1FF" stop-opacity="0"/></linearGradient></defs>
  <polygon points="0,${H} ${pts} ${W},${H}" fill="url(#g)"/>
  <polyline points="${pts}" fill="none" stroke="#7DB1FF" stroke-width="2" stroke-linejoin="round"/>`;}
(function(){const w=$('sparkwrap');w.addEventListener('mousemove',e=>{if(SP.length<2)return;
 const r=w.getBoundingClientRect(),fx=(e.clientX-r.left)/r.width;
 const xs=SP.map(p=>p[0]),x0=Math.min(...xs),x1=Math.max(...xs),t=x0+fx*(x1-x0);
 let best=SP[0];for(const p of SP)if(Math.abs(p[0]-t)<Math.abs(best[0]-t))best=p;
 const px=(best[0]-x0)/((x1-x0)||1)*r.width;
 $('xhair').style.display='block';$('xhair').style.left=px+'px';
 const tip=$('tip');tip.style.display='block';
 tip.textContent=new Date(best[0]*1000).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'})+'  ⌂ '+fmt(best[1]);
 tip.style.left=Math.min(px+10,r.width-140)+'px';tip.style.top='6px';});
 w.addEventListener('mouseleave',()=>{$('xhair').style.display='none';$('tip').style.display='none';});})();
function mdlite(s){return s.replace(/&/g,'&amp;').replace(/</g,'&lt;')
 .replace(/^## (.+)$/gm,'<span class="hd">$1</span>')
 .replace(/\*\*([^*]+)\*\*/g,'<span class="em">$1</span>');}
async function tick(){
 try{
  const d=await(await fetch('/data.json')).json();
  $('clock').textContent=d.ts;
  $('alive').className='pill '+(d.alive?'ok':'down');
  $('alive').textContent=d.alive?'● SUPERVISOR UP':'● SUPERVISOR DOWN';
  $('sesspill').textContent='SESSION '+(d.session.match(/session (\d+)/)||[,'?'])[1];
  const ss=d.session_state;
  if(ss.active){$('sessact').className='pill ok';
   $('sessact').textContent='◉ IN SESSION · '+Math.floor(ss.elapsed/60)+'m';}
  else if(d.queue>0){$('sessact').className='pill ok';
   $('sessact').textContent='○ NEXT SESSION IMMINENT · '+d.queue+' queued';}
  else{$('sessact').className='pill';
   $('sessact').textContent='○ NEXT HEARTBEAT ~'+Math.ceil(ss.eta/60)+'m';}
  setNum($('treasury'),d.treasury,fmt(d.treasury));
  $('delta').textContent='+'+fmt(d.delta24);$('rate').textContent='+'+fmt(d.rate);
  setNum($('ratek'),d.rate,'+'+fmt(d.rate));
  $('rateh').textContent=(d.rate/21900).toFixed(1)+'× original KPI';
  $('sessions').textContent=d.sessions_total;
  setNum($('queue'),d.queue,d.queue);
  $('queueh').innerHTML=d.queue>10?'<span class="qd">backlog building</span>':'nominal';
  $('odec').textContent=d.open_decisions;
  const K=n=>n>=1e6?(n/1e6).toFixed(1)+'M':n>=1e3?(n/1e3).toFixed(0)+'k':n;
  $('tokv').textContent=K(d.tokens.session.in+d.tokens.session.out);
  $('tokh').textContent='all-time '+K(d.tokens.total.in+d.tokens.total.out)+' ('+d.tokens.files+' sessions) · +'+K(d.tokens.total.cache)+' cached';
  if(d.gate&&d.gate.progress!=null){$('gatep').textContent=d.gate.progress.toFixed(1)+'%';
   $('gatemats').innerHTML=d.gate.materials.map(m=>{const pc=100*m.have/m.need;
    return `<div class="matrow"><span class="name">${m.name}</span><div class="bar"><i style="width:${Math.max(pc,.6)}%"></i></div><span>${m.have}/${m.need}</span></div>`}).join('');}
  drawSpark(d.spark);
  const mx=Math.max(...d.hourly.map(h=>Math.abs(h[1])),1);
  $('bars').innerHTML=d.hourly.map(h=>`<div class="b ${h[1]<0?'neg':''}" style="height:${Math.max(4,58*Math.abs(h[1])/mx)}px" data-t="${h[0]} · ${h[1]>=0?'+':''}${fmt(h[1])}"></div>`).join('');
  $('heads').innerHTML=d.heads.map(h=>`<li>${h.replace(/</g,'&lt;')}</li>`).join('');
  $('containers').innerHTML='<tr><th>container</th><th>stream</th><th>state</th><th>uptime</th></tr>'+
   d.containers.map(c=>`<tr><td>${c.id.slice(0,40)}</td><td><span class="tag ${TYPETAG(c.type)}">${c.type.replace(/_/g,' ').slice(0,22)}</span></td>
    <td><span class="dot d-${c.status}"></span>${c.status}</td><td>${up(c.up)}</td></tr>`).join('');
  $('ships').innerHTML='<tr><th>ship</th><th>nav</th><th>loc</th><th>fuel</th><th>cargo</th></tr>'+
   d.ships.map(s=>{const fp=s.fc?100*s.f/s.fc:100,cp=s.cc?100*s.c/s.cc:0;
    return `<tr><td>${s.sym}</td><td>${s.nav}</td><td>${s.loc}</td>
    <td><span class="meter ${fp<20?'low':''}"><i style="width:${fp}%"></i></span>${s.f}/${s.fc}</td>
    <td><span class="meter"><i style="width:${cp}%"></i></span>${s.c}/${s.cc}</td></tr>`}).join('');
  $('events').innerHTML='<tr><th>#</th><th>type</th><th>ship</th><th>at</th><th>state</th></tr>'+
   d.events.map(e=>`<tr><td>${e.id}</td><td>${e.type}</td><td>${e.ship}</td><td>${e.at}</td>
    <td class="st">${e.done?'<span style="color:var(--dim)">processed</span>':'<span class="st-new">queued</span>'}</td></tr>`).join('');
  $('reports').innerHTML='<tr><th>report</th><th>kind</th><th>status</th></tr>'+
   d.reports.map(r=>`<tr class="clickable ${r.closed?'closedrow':''}" data-name="${r.name}">
    <td>${r.name.slice(0,46)}</td><td>${r.kind}</td><td class="st st-${r.status}">${r.status.replace('_',' ')}</td></tr>`).join('');
  document.querySelectorAll('#reports tr.clickable').forEach(tr=>tr.onclick=()=>showReport(tr.dataset.name));
  $('sesshead').textContent='Latest log entry — '+d.session;
  $('entry').innerHTML=mdlite(d.last_entry);
  $('sup').innerHTML=d.supervisor.replace(/&/g,'&amp;').replace(/</g,'&lt;').split('\n')
   .map(l=>/error|failed/i.test(l)?`<span class="err">${l}</span>`:/complete|merged/i.test(l)?`<span class="ok">${l}</span>`:l).join('\n');
 }catch(e){$('alive').className='pill down';$('alive').textContent='● FETCH FAILED';}
}
async function showReport(name){
 $('mtitle').textContent=name;$('mbody').textContent='loading…';
 $('modal').classList.add('open');
 try{const r=await(await fetch('/report?name='+encodeURIComponent(name))).json();
  $('mbody').innerHTML=mdlite(r.body);}catch(e){$('mbody').textContent='failed to load';}}
$('mclose').onclick=()=>$('modal').classList.remove('open');
$('modal').onclick=e=>{if(e.target.id==='modal')$('modal').classList.remove('open');};
document.addEventListener('keydown',e=>{if(e.key==='Escape')$('modal').classList.remove('open');});
tick();setInterval(tick,5000);
</script></body></html>"""

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        if self.path == "/data.json":
            body, ctype = json.dumps(collect()).encode(), "application/json"
        elif self.path.startswith("/report?name="):
            name = self.path.split("=", 1)[1]
            if not re.fullmatch(r"[A-Za-z0-9._-]+", name):
                body, ctype = b"bad name", "text/plain"
            else:
                bugdir = os.path.join(CAPTAIN, "reports", "bugs")
                content = ""
                for d in (bugdir, os.path.join(bugdir, "closed")):
                    fp = os.path.join(d, name + ".md")
                    if os.path.isfile(fp):
                        content = read(fp)
                        gl = fp + ".gate.log"
                        if os.path.isfile(gl):
                            content += "\n\n--- GATE LOG (tail) ---\n" + read(gl, 2000)
                        break
                body, ctype = json.dumps({"name": name, "body": content or "(not found)"}).encode(), "application/json"
        else:
            body, ctype = PAGE.encode(), "text/html; charset=utf-8"
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

if __name__ == "__main__":
    print("captain console: http://localhost:8899")
    HTTPServer(("127.0.0.1", 8899), H).serve_forever()
