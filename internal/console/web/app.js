/* bouncewarden dashboard — vanilla JS SPA fed by /api/state + /api/events (SSE) */
"use strict";

const S = {
  snap: null, page: "overview", inst: "__all",
  hist: [], histT: 0, es: null, ageTimer: null, formDirty: false,
};

const $ = (s) => document.querySelector(s);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) =>
  ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const num = (v, d) => (v === null || v === undefined || v < 0)
  ? '<span class="unavail">–</span>' : Number(v).toLocaleString(undefined, { maximumFractionDigits: d ?? 0 });
const plain = (v, d) => (v === null || v === undefined || v < 0) ? "–" : Number(v).toFixed(d ?? 0);
const bytes = (v) => {
  if (v === null || v === undefined || v < 0) return '<span class="unavail">–</span>';
  const u = ["B/s", "kB/s", "MB/s", "GB/s"]; let i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v.toFixed(v >= 100 ? 0 : 1) + " " + u[i];
};
const ago = (t) => {
  const s = (Date.now() - new Date(t).getTime()) / 1000;
  return s < 1 ? "now" : s < 60 ? s.toFixed(0) + "s" : s < 3600 ? (s / 60).toFixed(0) + "m" : (s / 3600).toFixed(1) + "h";
};
const toast = (msg, cls) => {
  const d = document.createElement("div");
  d.className = "toast " + (cls || "");
  d.textContent = msg;
  $("#toast").appendChild(d);
  setTimeout(() => d.remove(), 4500);
};

/* ---------- data ---------- */
function instances() { return (S.snap && S.snap.instances) || []; }
function selected() {
  return S.inst === "__all" ? instances() : instances().filter((i) => i.name === S.inst);
}
function findings() {
  const f = (S.snap && S.snap.findings) || [];
  return S.inst === "__all" ? f : f.filter((x) => x.instance === S.inst);
}

async function api(path, opts) {
  const r = await fetch(path, opts);
  if (r.status === 401) { location.reload(); throw new Error("unauthenticated"); }
  const txt = await r.text();
  let j; try { j = JSON.parse(txt); } catch { j = { error: txt }; }
  if (!r.ok) throw new Error(j.error || ("HTTP " + r.status));
  return j;
}

async function load() {
  try {
    const [snap, hist] = await Promise.all([
      api("/api/state"), api("/api/history?n=900"),
    ]);
    S.snap = snap.schema ? snap : null;
    S.hist = hist.points || [];
    render();
  } catch (e) { console.error(e); }
}

function connect() {
  if (S.es) S.es.close();
  const es = new EventSource("/api/events");
  S.es = es;
  es.onmessage = (m) => {
    try {
      const ev = JSON.parse(m.data);
      if (ev.kind === "snapshot") {
        S.snap = ev.data;
        appendHist(ev.data);
        // Don't wipe a half-typed Configuration form every 2s.
        if (!(S.page === "configuration" && S.formDirty)) render();
      } else if (ev.kind === "audit") {
        const a = ev.data;
        toast(`${a.ok ? "✓" : "✗"} ${a.verb}${a.database ? " " + a.database : ""} on ${a.instance} — ${a.ok ? (a.result || "done") : a.error}`, a.ok ? "ok" : "err");
        if (S.page === "audit") render();
      }
    } catch (e) { /* heartbeat */ }
  };
  es.onerror = () => {
    const b = $("#liveBadge");
    b.textContent = "DISCONNECTED"; b.className = "badge stale";
  };
}

function appendHist(snap) {
  const p = { taken_at: snap.taken_at, instances: {} };
  for (const inx of snap.instances || []) {
    const s = inx.summary || {}, r = inx.total_rate || {};
    p.instances[inx.name] = {
      reachable: inx.reachable, cl_active: s.cl_active || 0,
      cl_waiting: s.cl_waiting || 0, sv_active: s.sv_active || 0,
      sv_idle: s.sv_idle || 0, max_wait: s.max_wait ?? -1,
      xact_per_sec: r.xact_per_sec ?? -1, query_per_sec: r.query_per_sec ?? -1,
      bytes_in_per_sec: r.bytes_in_per_sec ?? -1, bytes_out_per_sec: r.bytes_out_per_sec ?? -1,
    };
  }
  S.hist.push(p);
  if (S.hist.length > 1800) S.hist = S.hist.slice(-1800);
}

/* ---------- header ---------- */
function renderHeader() {
  const sel = $("#instSel");
  const cur = sel.value || S.inst;
  sel.innerHTML = '<option value="__all">All instances</option>' +
    instances().map((i) => `<option value="${esc(i.name)}">${esc(i.name)}</option>`).join("");
  sel.value = [...sel.options].some((o) => o.value === cur) ? cur : "__all";
  S.inst = sel.value;
}
$("#instSel")?.addEventListener("change", (e) => { S.inst = e.target.value; render(); });

setInterval(() => {
  if (!S.snap) return;
  const b = $("#liveBadge"), a = $("#ageBadge");
  const age = (Date.now() - new Date(S.snap.taken_at).getTime()) / 1000;
  a.textContent = age.toFixed(0) + "s ago";
  if (age > 10) { b.textContent = "STALE"; b.className = "badge stale"; }
  else { b.textContent = "LIVE"; b.className = "badge live"; }
}, 1000);

$("#themeBtn").addEventListener("click", () => {
  const h = document.documentElement;
  h.dataset.theme = h.dataset.theme === "dark" ? "light" : "dark";
});

/* ---------- routing ---------- */
function route() {
  S.page = (location.hash || "#overview").slice(1);
  document.querySelectorAll("#nav a").forEach((a) =>
    a.classList.toggle("active", a.dataset.page === S.page));
  render();
}
window.addEventListener("hashchange", route);

/* ---------- chart ---------- */
function chart(el, series, opts = {}) {
  const W = 700, H = 170, P = { l: 46, r: 8, t: 10, b: 20 };
  const pts = series.flatMap((s) => s.data).filter((v) => v >= 0);
  const max = Math.max(...pts, 0.001) * 1.1;
  const n = Math.max(...series.map((s) => s.data.length), 2);
  const x = (i) => P.l + (i / (n - 1)) * (W - P.l - P.r);
  const y = (v) => P.t + (1 - Math.max(0, v) / max) * (H - P.t - P.b);
  let grid = "";
  for (let g = 0; g <= 3; g++) {
    const gy = P.t + (g / 3) * (H - P.t - P.b);
    const gv = max * (1 - g / 3);
    grid += `<line x1="${P.l}" y1="${gy}" x2="${W - P.r}" y2="${gy}" stroke="var(--border)" stroke-width=".5"/>` +
      `<text x="${P.l - 5}" y="${gy + 3}" fill="var(--muted)" font-size="9" text-anchor="end">${(opts.fmt || plain)(gv)}</text>`;
  }
  const paths = series.map((s) => {
    let d = "", pen = false;
    s.data.forEach((v, i) => {
      if (v < 0) { pen = false; return; }
      d += (pen ? "L" : "M") + x(i).toFixed(1) + "," + y(v).toFixed(1);
      pen = true;
    });
    return `<path d="${d}" fill="none" stroke="${s.color}" stroke-width="1.6"/>`;
  }).join("");
  const legend = series.map((s, i) =>
    `<span style="color:${s.color}">● ${esc(s.name)}</span>`).join(" · ");
  el.innerHTML = `<div class="muted" style="font-size:11px;margin-bottom:4px">${legend}</div>` +
    `<svg class="chart" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">${grid}${paths}</svg>`;
}

function seriesFor(key, agg) {
  // Build one series summed across selected instances.
  const data = S.hist.map((p) => {
    let sum = 0, any = false;
    for (const inx of selected()) {
      const ip = p.instances[inx.name];
      if (ip && ip[key] >= 0) { sum += ip[key]; any = true; }
    }
    return any ? sum : -1;
  });
  return data;
}

/* ---------- pages ---------- */
function render() {
  renderHeader();
  const m = $("#main");
  const fn = {
    overview: pageOverview, topology: pageTopology, pools: pagePools,
    connections: pageConnections,
    operations: pageOperations, configuration: pageConfiguration, audit: pageAudit,
  }[S.page] || pageOverview;
  m.innerHTML = "";
  fn(m);
}

function pageOverview(m) {
  const inst = selected();
  if (!inst.length) { m.innerHTML = `<div class="panel empty">No instances configured — open <a href="#configuration">Configuration</a> to add a PgBouncer endpoint.</div>`; return; }

  let clA = 0, clW = 0, svA = 0, svI = 0, maxW = -1, conns = 0, maxConn = 0, paused = false, reach = 0;
  for (const i of inst) {
    if (i.reachable) reach++;
    const s = i.summary || {};
    clA += s.cl_active || 0; clW += s.cl_waiting || 0;
    svA += s.sv_active || 0; svI += s.sv_idle || 0;
    if ((s.max_wait ?? -1) > maxW) maxW = s.max_wait;
    conns += s.client_conns || 0; maxConn += s.max_client_conn || 0;
    if (i.paused) paused = true;
  }
  const card = (k, v, cls, sub) =>
    `<div class="card ${cls || ""}"><div class="k">${k}</div><div class="v">${v}</div><div class="sub">${sub || ""}</div></div>`;
  const wCls = clW > 0 ? "crit" : "ok";

  let html = `<div class="grid cards">
    ${card("Instances reachable", reach + "/" + inst.length, reach === inst.length ? "ok" : "crit")}
    ${card("Active clients", num(clA), clA > 0 ? "" : "")}
    ${card("Waiting clients", num(clW), wCls)}
    ${card("Server links", num(svA + svI), "", svA + " active · " + svI + " idle")}
    ${card("Max client wait", maxW < 0 ? '<span class="unavail">–</span>' : (maxW * 1000).toFixed(0) + " ms", maxW > 0.25 ? "crit" : maxW > 0.05 ? "warn" : "ok")}
    ${card("Paused", paused ? "YES" : "no", paused ? "warn" : "")}
    ${card("Client conns", num(conns), "", maxConn ? "of " + num(maxConn) : "")}
  </div>`;

  html += `<div class="two" style="margin-top:14px">
    <div class="panel"><h2>Throughput <span class="sub">counter-delta rates · xact/s &amp; query/s</span></h2><div id="chX"></div></div>
    <div class="panel"><h2>Bytes <span class="sub">received / sent per second</span></h2><div id="chB"></div></div>
  </div>`;

  html += `<div class="two">
    <div class="panel"><h2>Pool pressure</h2>${poolTable(inst)}</div>
    <div class="panel"><h2>Active findings</h2>${findingsList()}</div>
  </div>`;
  m.innerHTML = html;

  chart($("#chX"), [
    { name: "xact/s", color: "#60a5fa", data: seriesFor("xact_per_sec") },
    { name: "query/s", color: "#34d399", data: seriesFor("query_per_sec") },
  ]);
  chart($("#chB"), [
    { name: "bytes in", color: "#f59e0b", data: seriesFor("bytes_in_per_sec") },
    { name: "bytes out", color: "#a78bfa", data: seriesFor("bytes_out_per_sec") },
  ], { fmt: (v) => v >= 1024 * 1024 ? (v / 1048576).toFixed(1) + "M" : v >= 1024 ? (v / 1024).toFixed(0) + "k" : v.toFixed(0) });
}

function poolTable(inst) {
  let rows = "";
  for (const i of inst) {
    const dbs = Object.fromEntries((i.databases || []).map((d) => [d.name, d]));
    for (const p of i.pools || []) {
      const d = dbs[p.database] || {};
      const used = p.sv_active + p.sv_idle;
      const util = d.pool_size > 0 ? Math.round(used / d.pool_size * 100) + "%" : '<span class="unavail">–</span>';
      rows += `<tr>
        <td>${esc(i.name)}${i.stale ? ' <span class="badge stale">stale</span>' : ""}</td>
        <td>${esc(p.database)}</td><td>${esc(p.user)}</td>
        <td><span class="badge">${esc(p.pool_mode || "–")}</span></td>
        <td class="num">${num(p.cl_active)}</td><td class="num">${p.cl_waiting > 0 ? `<b style="color:var(--crit)">${num(p.cl_waiting)}</b>` : num(p.cl_waiting)}</td>
        <td class="num">${num(p.sv_active)}</td><td class="num">${num(p.sv_idle)}</td>
        <td class="num">${util}</td>
        <td class="num">${p.max_wait < 0 ? '<span class="unavail">–</span>' : (p.max_wait * 1000).toFixed(0) + "ms"}</td>
      </tr>`;
    }
  }
  if (!rows) return '<div class="empty">No pools observed yet.</div>';
  return `<div class="tablewrap"><table><thead><tr>
    <th>Instance</th><th>Database</th><th>User</th><th>Mode</th>
    <th class="num">cl_act</th><th class="num">cl_wait</th>
    <th class="num">sv_act</th><th class="num">sv_idle</th>
    <th class="num">util</th><th class="num">maxwait</th>
  </tr></thead><tbody>${rows}</tbody></table></div>`;
}

function findingsList() {
  const f = findings();
  if (!f.length) return '<div class="empty">No active findings.</div>';
  return f.map((x) => `<div class="finding">
    <span class="sev ${x.severity}">${x.severity}</span>
    <span><b>${esc(x.instance)}</b>${x.pool ? " · " + esc(x.pool) : ""} — ${esc(x.message)}
    <div class="ev">${esc(x.evidence || "")} · since ${ago(x.first_seen)}</div></span>
  </div>`).join("");
}

function pagePools(m) {
  const inst = selected();
  let html = `<div class="panel"><h2>Pools <span class="sub">SHOW POOLS per instance · utilisation vs pool_size</span></h2>${poolTable(inst)}</div>`;
  html += `<div class="panel"><h2>Per-database rates <span class="sub">counter-delta, window shown per instance</span></h2>`;
  let rows = "";
  for (const i of inst) {
    const rates = i.rates || {};
    const dbs = Object.fromEntries((i.databases || []).map((d) => [d.name, d]));
    for (const [db, r] of Object.entries(rates)) {
      const d = dbs[db] || {};
      rows += `<tr><td>${esc(i.name)}</td><td>${esc(db)}</td>
        <td>${esc(d.host || "–")}:${d.port || ""}</td>
        <td><span class="badge">${esc(d.pool_mode || "–")}</span>${d.paused ? ' <span class="badge warn">paused</span>' : ""}${d.disabled ? ' <span class="badge crit">disabled</span>' : ""}</td>
        <td class="num">${num(d.pool_size)}</td><td class="num">${num(d.current_connections)}</td><td class="num">${num(d.current_client_connections)}</td>
        <td class="num">${plain(r.xact_per_sec, 1)}</td><td class="num">${plain(r.query_per_sec, 1)}</td>
        <td class="num">${bytes(r.bytes_in_per_sec)}</td><td class="num">${bytes(r.bytes_out_per_sec)}</td>
      </tr>`;
    }
  }
  html += rows ? `<div class="tablewrap"><table><thead><tr>
    <th>Instance</th><th>Database</th><th>Backend</th><th>Mode/Flags</th>
    <th class="num">pool_size</th><th class="num">srv conns</th><th class="num">cl conns</th>
    <th class="num">xact/s</th><th class="num">query/s</th><th class="num">B/s in</th><th class="num">B/s out</th>
  </tr></thead><tbody>${rows}</tbody></table></div>` : '<div class="empty">No rate data yet.</div>';
  html += `</div>`;
  m.innerHTML = html;
}

function pageConnections(m) {
  const inst = selected();
  const conn = (list, isServer) => {
    let rows = "";
    for (const i of inst) {
      for (const c of list(i) || []) {
        const ageS = c.connect_time ? ago(c.connect_time) : "–";
        rows += `<tr><td>${esc(i.name)}</td><td>${esc(c.user)}</td><td>${esc(c.database)}</td>
          <td><span class="badge ${c.state === "waiting" ? "warn" : ""}">${esc(c.state)}</span></td>
          <td class="mono">${esc(c.addr)}:${c.port || ""}</td>
          <td class="num">${c.wait < 0 ? '<span class="unavail">–</span>' : (c.wait * 1000).toFixed(0) + "ms"}</td>
          <td>${ageS}</td><td>${c.tls ? "TLS" : '<span class="muted">plain</span>'}</td>
          ${isServer ? `<td>${c.close_needed ? '<span class="badge warn">close_needed</span>' : ""}</td>` : `<td class="mono">${esc(c.app_name || "")}</td>`}
        </tr>`;
      }
    }
    return rows || `<tr><td colspan="9" class="empty">none</td></tr>`;
  };
  m.innerHTML = `<div class="two">
    <div class="panel"><h2>Client connections <span class="sub">SHOW CLIENTS</span></h2>
      <div class="tablewrap"><table><thead><tr><th>Inst</th><th>User</th><th>DB</th><th>State</th><th>Addr</th><th class="num">Wait</th><th>Age</th><th>TLS</th><th>App</th></tr></thead>
      <tbody>${conn((i) => i.clients, false)}</tbody></table></div></div>
    <div class="panel"><h2>Server connections <span class="sub">SHOW SERVERS — backend links</span></h2>
      <div class="tablewrap"><table><thead><tr><th>Inst</th><th>User</th><th>DB</th><th>State</th><th>Backend</th><th class="num">Wait</th><th>Age</th><th>TLS</th><th>Flags</th></tr></thead>
      <tbody>${conn((i) => i.servers, true)}</tbody></table></div></div>
  </div>`;
}

/* ---------- topology ---------- */
/* clients → pooler → logical pool → backend. Solid edges = observed
   connections; dashed = configured target with no observed links.
   Edges show connection counts only — per-edge byte rates are not
   attributable from SHOW STATS (database-level), so none are drawn. */
let topoScale = 1;
function pageTopology(m) {
  const inst = selected();
  if (!inst.length) { m.innerHTML = `<div class="panel empty">No instances configured.</div>`; return; }
  m.innerHTML = `<div class="panel"><h2>Topology <span class="sub">observed connection graph per instance</span></h2>
    <div style="display:flex;gap:8px;align-items:center;margin-bottom:10px;flex-wrap:wrap">
      <input id="topoSearch" placeholder="search nodes…" style="width:180px">
      <button class="ghost small" id="tzIn">+</button>
      <button class="ghost small" id="tzOut">−</button>
      <button class="ghost small" id="tzFit">fit</button>
      <span class="muted" style="font-size:11px">solid = observed links · dashed = configured, no traffic · amber = waiting</span>
    </div>
    <div id="topoWrap" style="overflow:auto"><svg id="topoSvg" class="chart" style="height:auto"></svg></div>
    <div id="topoDetail" class="probebox hidden" style="margin-top:10px"></div>
  </div>`;
  drawTopology();
  $("#topoSearch").addEventListener("input", drawTopology);
  $("#tzIn").onclick = () => { topoScale = Math.min(3, topoScale * 1.25); applyTopoSize(); };
  $("#tzOut").onclick = () => { topoScale = Math.max(0.4, topoScale / 1.25); applyTopoSize(); };
  $("#tzFit").onclick = () => { topoScale = 1; applyTopoSize(); };
}

function applyTopoSize() {
  const svg = $("#topoSvg");
  if (svg) svg.style.width = (svg.dataset.w * topoScale) + "px";
}

function drawTopology() {
  const q = ($("#topoSearch")?.value || "").toLowerCase();
  const NW = 190, NH = 40, GAP = 12, ROWH = NH + GAP, BAND = 34;
  const CX = { client: 30, inst: 350, pool: 590, back: 840 };
  const W = 1060;
  const det = []; // detail records, index = data-di

  const match = (t) => !q || String(t).toLowerCase().includes(q);
  const dim = (t) => q && !match(t) ? ' opacity="0.22"' : "";

  let y = 20, svg = "";
  for (const inx of selected()) {
    // client groups: addr + app + db
    const gmap = {};
    for (const c of inx.clients || []) {
      const k = c.addr + "|" + (c.app_name || "") + "|" + c.database;
      const g = gmap[k] = gmap[k] || { addr: c.addr, app: c.app_name, db: c.database, n: 0, act: 0, wait: 0 };
      g.n++;
      if (c.state === "waiting") g.wait++;
      else g.act++;
    }
    const clients = Object.values(gmap);
    const pools = (inx.pools || []).filter((p) => p.database !== "pgbouncer");
    const dbRow = Object.fromEntries((inx.databases || []).map((d) => [d.name, d]));
    const srvByDb = {};
    for (const s of inx.servers || []) {
      const e = srvByDb[s.database] = srvByDb[s.database] || { n: 0, addrs: {}, states: {} };
      e.n++;
      e.addrs[s.addr + ":" + s.port] = (e.addrs[s.addr + ":" + s.port] || 0) + 1;
      e.states[s.state] = (e.states[s.state] || 0) + 1;
    }
    const backs = (inx.databases || []).filter((d) => d.host && d.name !== "pgbouncer");

    const rows = Math.max(clients.length, pools.length, backs.length, 1);
    const bandH = rows * ROWH + BAND;
    const cy = (i) => y + BAND + i * ROWH + NH / 2;

    // band header
    const hdr = `${inx.name} · ${inx.host}:${inx.port} · ${inx.version || "?"}${inx.paused ? " · PAUSED" : ""}${inx.suspended ? " · SUSPENDED" : ""}${inx.stale ? " · STALE" : ""}`;
    svg += `<g${dim(inx.name)}><rect x="14" y="${y}" width="${W - 28}" height="${bandH}" rx="8" fill="var(--panel2)" stroke="var(--border)" stroke-width=".6"/>` +
      `<text x="26" y="${y + 22}" fill="var(--muted)" font-size="12" font-weight="600">${esc(hdr)}</text></g>`;

    const node = (di, x, ny, title, sub, stroke, fill = "var(--panel)") => {
      return `<g data-di="${di}"${dim(title)} style="cursor:pointer">
        <rect x="${x}" y="${ny - NH / 2}" width="${NW}" height="${NH}" rx="7" fill="${fill}" stroke="${stroke}" stroke-width="1.4"/>
        <text x="${x + 9}" y="${ny - 4}" fill="var(--text)" font-size="11.5" font-weight="600">${esc(title)}</text>
        <text x="${x + 9}" y="${ny + 11}" fill="var(--muted)" font-size="9.5">${esc(sub)}</text></g>`;
    };
    const edge = (x1, y1, x2, y2, dashed, warn, label) => {
      const mx = (x1 + x2) / 2;
      return `<path d="M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}" fill="none"
          stroke="${warn ? "var(--warn)" : "#64748b"}" stroke-width="${warn ? 2 : 1.1}" ${dashed ? 'stroke-dasharray="4 3"' : ""} opacity=".85"/>` +
        (label ? `<text x="${mx}" y="${(y1 + y2) / 2 - 3}" fill="${warn ? "var(--warn)" : "var(--muted)"}" font-size="9" text-anchor="middle">${esc(label)}</text>` : "");
    };
    const reg = (title, rows) => { det.push({ title, rows }); return det.length - 1; };

    // instance node
    const instY = y + BAND + (rows * ROWH - ROWH) / 2 + NH / 2;
    const iStroke = !inx.reachable ? "var(--crit)" : inx.paused || inx.suspended ? "var(--warn)" : "var(--accent)";
    const iSub = (inx.version || "").replace("PgBouncer ", "v") +
      (inx.paused ? " · PAUSED" : inx.suspended ? " · SUSPENDED" : inx.reachable ? " · active" : " · DOWN");
    const instDi = reg(inx.name, [
      ["endpoint", `${inx.host}:${inx.port}`], ["version", inx.version || "–"],
      ["state", inx.paused ? "paused" : inx.suspended ? "suspended" : inx.reachable ? "active" : "unreachable"],
      ["collected", inx.collected_at ? new Date(inx.collected_at).toLocaleTimeString() : "–"],
      ["clients", String((inx.clients || []).length)], ["server links", String((inx.servers || []).length)],
    ]);

    // client group nodes + edges → instance
    clients.forEach((g, i) => {
      const ny = cy(i);
      const di = reg(`${g.addr} · ${g.app || "client"}`, [
        ["source addr", g.addr], ["app", g.app || "–"], ["database", g.db],
        ["connections", String(g.n)], ["active", String(g.act)], ["waiting", String(g.wait)],
      ]);
      svg += node(di, CX.client, ny, `${g.addr} · ${g.app || "?"}`, `${g.n} conns · ${g.act} act · ${g.wait} wait`, g.wait ? "var(--warn)" : "var(--border)");
      svg += edge(CX.client + NW, ny, CX.inst, instY, false, g.wait > 0, `${g.n} conn`);
    });

    // pool nodes + edges: instance→pool (membership), pool→backend
    pools.forEach((p, i) => {
      const ny = cy(i);
      const d = dbRow[p.database] || {};
      const sv = srvByDb[p.database] || { n: 0 };
      const di = reg(`${p.database}/${p.user}`, [
        ["pool", `${p.database}/${p.user}`], ["mode", p.pool_mode || d.pool_mode || "–"],
        ["cl_active", String(p.cl_active)], ["cl_waiting", String(p.cl_waiting)],
        ["sv_active", String(p.sv_active)], ["sv_idle", String(p.sv_idle)],
        ["pool_size", String(d.pool_size ?? "–")], ["maxwait", p.max_wait < 0 ? "–" : (p.max_wait * 1000).toFixed(0) + " ms"],
      ]);
      svg += edge(CX.inst + NW, instY, CX.pool, ny, false, false, "");
      svg += node(di, CX.pool, ny, `${p.database} / ${p.user}`,
        `${p.pool_mode || d.pool_mode || "?"} · act ${p.cl_active} wait ${p.cl_waiting} · sv ${p.sv_active + p.sv_idle}`,
        p.cl_waiting ? "var(--warn)" : "var(--border)");
      if (d.host) {
        const bi = backs.findIndex((b) => b.name === d.name);
        if (bi >= 0) {
          const by = cy(bi);
          svg += edge(CX.pool + NW, ny, CX.back, by, sv.n === 0, p.cl_waiting > 0, `${sv.n} srv`);
        }
      }
    });

    // backend destination nodes
    backs.forEach((d, i) => {
      const ny = cy(i);
      const sv = srvByDb[d.name] || { n: 0, addrs: {}, states: {} };
      const rate = (inx.stats || {})[d.name];
      const di = reg(`${d.name} → ${d.host}`, [
        ["database", d.name], ["configured host(s)", `${d.host}:${d.port}`],
        ["pool_size", String(d.pool_size)], ["reserve_pool", String(d.reserve_pool)],
        ["observed server links", String(sv.n)],
        ["link states", Object.entries(sv.states).map(([k, v]) => `${k}:${v}`).join(" ") || "–"],
        ["resolved addrs", Object.entries(sv.addrs).map(([k, v]) => `${k}×${v}`).join(" ") || "–"],
        ["paused", d.paused ? "yes" : "no"], ["disabled", d.disabled ? "yes" : "no"],
        ["db avg xact/s", rate && rate.avg_xact_count >= 0 ? String(Math.round(rate.avg_xact_count)) : "–"],
      ]);
      const stroke = d.disabled ? "var(--crit)" : d.paused ? "var(--warn)" : sv.n === 0 ? "var(--border)" : "#64748b";
      svg += node(di, CX.back, ny, `${d.name} → ${d.host}`, `:${d.port} · ${sv.n} srv links${sv.n === 0 ? " (idle)" : ""}`, stroke);
    });

    svg += node(instDi, CX.inst, instY, inx.name, iSub, iStroke);
    y += bandH + 26;
  }

  const svgEl = $("#topoSvg");
  svgEl.dataset.w = W;
  svgEl.setAttribute("viewBox", `0 0 ${W} ${y}`);
  svgEl.setAttribute("width", W * topoScale);
  svgEl.style.width = W * topoScale + "px";
  svgEl.innerHTML = svg;
  svgEl.onclick = (e) => {
    const g = e.target.closest("g[data-di]");
    const box = $("#topoDetail");
    if (!g) { box.classList.add("hidden"); return; }
    const d = det[+g.dataset.di];
    box.classList.remove("hidden");
    box.innerHTML = `<b>${esc(d.title)}</b>\n` + d.rows.map(([k, v]) => `${k.padEnd(24)}${esc(v)}`).join("\n");
  };
}

/* ---------- operations console ---------- */
const OPS = [
  { verb: "PAUSE", desc: "Stop giving out server connections; clients queue", cls: "warnbtn", db: true },
  { verb: "RESUME", desc: "Resume a paused pooler/database", cls: "", db: true },
  { verb: "SUSPEND", desc: "Administrative suspend of all sockets", cls: "warnbtn", db: false },
  { verb: "RELOAD", desc: "Reload pgbouncer.ini into the running process", cls: "", db: false },
  { verb: "RECONNECT", desc: "Recycle backend server connections", cls: "", db: true },
  { verb: "DISABLE", desc: "Reject new client queries on a database", cls: "warnbtn", db: "required" },
  { verb: "ENABLE", desc: "Re-enable a disabled database", cls: "", db: "required" },
  { verb: "SHUTDOWN", desc: "Shut the pooler process down (CRIT)", cls: "danger", db: false },
];

function pageOperations(m) {
  const inst = selected();
  if (!inst.length) { m.innerHTML = `<div class="panel empty">No instances.</div>`; return; }
  let html = "";
  for (const i of inst) {
    const canAdmin = true; // enforced server-side via Admin flag
    const state = i.paused ? '<span class="badge warn">PAUSED</span>' :
      i.suspended ? '<span class="badge warn">SUSPENDED</span>' :
      i.reachable ? '<span class="badge live">up</span>' : '<span class="badge stale">down</span>';
    html += `<div class="panel"><h2>${esc(i.name)} <span class="sub">${esc(i.host)}:${i.port} · ${esc(i.version || "unknown version")} ${state}</span></h2>
      <div class="opbtns">${OPS.map((o) =>
        `<button class="${o.cls}" data-verb="${o.verb}" data-inst="${esc(i.name)}" data-dbreq="${o.db}" ${!i.reachable ? "disabled" : ""}>${o.verb}</button>`).join("")}
      </div>
      <div class="muted" style="font-size:12px;margin-top:8px">Database-scoped verbs prompt for a target database. Every action requires typed confirmation and is journaled to the audit log.</div>
      <div id="opmsg-${esc(i.name)}" style="margin-top:8px"></div>
    </div>`;
  }
  m.innerHTML = html;
  m.querySelectorAll("button[data-verb]").forEach((b) =>
    b.addEventListener("click", () => opDialog(b.dataset.inst, b.dataset.verb, b.dataset.dbreq)));
}

function opDialog(inst, verb, dbreq) {
  const i = instances().find((x) => x.name === inst);
  const dbs = (i && i.databases) || [];
  const pickDb = (cb) => {
    if (dbreq === false || dbreq === "false") return cb("");
    if (dbreq === "required" || !dbs.length) {
      const names = dbs.map((d) => d.name).filter((n) => n !== "pgbouncer");
      showModal(`${verb} database`, `<p>Target database on <b>${esc(inst)}</b>:</p>` +
        `<select id="modalDb">${names.map((n) => `<option>${esc(n)}</option>`).join("")}</select>`,
        { confirm: false, label: "Continue" }).then((ok) => {
        if (ok) cb($("#modalDb") ? $("#modalDb").value : names[0] || "");
      });
    } else {
      const names = [""].concat(dbs.map((d) => d.name).filter((n) => n !== "pgbouncer"));
      showModal(`${verb} scope`, `<p>Apply to one database, or blank for whole instance:</p>` +
        `<select id="modalDb">${names.map((n) => `<option value="${esc(n)}">${n ? esc(n) : "(whole instance)"}</option>`).join("")}</select>`,
        { confirm: false, label: "Continue" }).then((ok) => {
        if (ok) cb($("#modalDb").value);
      });
    }
  };
  pickDb((db) => {
    const expect = verb + " " + inst + (db ? "/" + db : "");
    showModal(`Confirm ${verb}`, `<p>This sends <code>${verb}${db ? " " + esc(db) : ""}</code> to <b>${esc(inst)}</b> through the PgBouncer admin console.</p>`,
      { confirm: true, expect }).then(async (ok) => {
      if (!ok) return;
      try {
        const res = await api(`/api/ops/${encodeURIComponent(inst)}/${verb}`, {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ database: db || "", confirm: expect }),
        });
        if (res.ok) toast(`${verb} on ${inst}: OK`, "ok");
        else toast(`${verb} failed: ${res.error || "rejected"}`, "err");
      } catch (e) { toast(e.message, "err"); }
    });
  });
}

/* ---------- configuration console ---------- */
async function pageConfiguration(m) {
  let inst = [], th = {};
  try {
    [inst, th] = await Promise.all([
      api("/api/instances").then((r) => r.instances || []),
      api("/api/thresholds"),
    ]);
  } catch (e) { m.innerHTML = `<div class="panel empty">${esc(e.message)}</div>`; return; }

  let rows = inst.map((i) => `<tr>
    <td><b>${esc(i.name)}</b>${i.env ? ` <span class="badge">${esc(i.env)}</span>` : ""}</td>
    <td class="mono">${esc(i.host)}:${i.port}</td><td>${esc(i.user)}</td><td>${esc(i.dbname)}</td>
    <td>${i.enabled ? '<span class="badge live">on</span>' : '<span class="badge">off</span>'}</td>
    <td>${i.admin ? '<span class="badge warn">admin</span>' : '<span class="muted">stats only</span>'}</td>
    <td>${i.has_password ? "stored" : '<span class="muted">none</span>'}</td>
    <td>
      <button class="small ghost" data-edit="${esc(i.name)}">Edit</button>
      <button class="small ghost" data-test="${esc(i.name)}">Test</button>
      <button class="small danger" data-del="${esc(i.name)}">Delete</button>
    </td></tr>`).join("");

  m.innerHTML = `
  <div class="panel"><h2>Monitored instances <span class="sub">machines on the live dashboard</span></h2>
    ${rows ? `<div class="tablewrap"><table><thead><tr>
      <th>Name</th><th>Endpoint</th><th>User</th><th>DB</th><th>Mon</th><th>Rights</th><th>Password</th><th></th>
    </tr></thead><tbody>${rows}</tbody></table></div>` : '<div class="empty">No instances yet — add one below.</div>'}
  </div>

  <div class="two">
  <div class="panel"><h2 id="formTitle">Add instance</h2>
    <div class="form-row">
      <div><label>Name</label><input id="f_name" placeholder="pgb1"></div>
      <div><label>Environment tag</label><input id="f_env" placeholder="prod"></div>
    </div>
    <div class="form-row">
      <div><label>Host</label><input id="f_host" placeholder="pgbouncer.internal"></div>
      <div><label>Port</label><input id="f_port" type="number" value="6432"></div>
    </div>
    <div class="form-row">
      <div><label>User</label><input id="f_user" placeholder="stats_user"></div>
      <div><label>Password <span class="muted">(write-only; blank keeps stored)</span></label><input id="f_pass" type="password" autocomplete="new-password"></div>
    </div>
    <div class="form-row">
      <div><label>Admin DB</label><input id="f_db" value="pgbouncer"></div>
      <div><label>&nbsp;</label><div style="display:flex;gap:14px;padding-top:8px">
        <label style="margin:0"><input type="checkbox" id="f_enabled" checked> enabled</label>
        <label style="margin:0"><input type="checkbox" id="f_admin"> admin rights (ops verbs)</label>
      </div></div>
    </div>
    <div style="display:flex;gap:8px;margin-top:14px">
      <button id="f_save" class="primary">Save</button>
      <button id="f_probe" class="ghost">Test connection</button>
      <button id="f_reset" class="ghost hidden">Cancel edit</button>
    </div>
    <div id="probeOut"></div>
  </div>

  <div class="panel"><h2>Finding thresholds</h2>
    <div class="form-row">
      <div><label>Wait warn (ms)</label><input id="t_ww" type="number" value="${th.wait_warn_ms}"></div>
      <div><label>Wait crit (ms)</label><input id="t_wc" type="number" value="${th.wait_crit_ms}"></div>
    </div>
    <div class="form-row">
      <div><label>Waiting clients warn</label><input id="t_cw" type="number" value="${th.waiting_clients_warn}"></div>
      <div><label>Client util warn %</label><input id="t_cu" type="number" value="${th.client_util_warn_pct}"></div>
    </div>
    <div class="form-row">
      <div><label>FD warn %</label><input id="t_fd" type="number" value="${th.fd_warn_pct}"></div><div></div>
    </div>
    <div style="margin-top:14px"><button id="t_save" class="primary">Save thresholds</button></div>
    <p class="muted" style="font-size:12px;margin-top:14px">Saving monitoring settings never reloads or mutates PgBouncer. Passwords are stored in the config volume and never returned to the browser.</p>
  </div>
  </div>`;

  let editing = null;
  const fill = (i) => {
    editing = i ? i.name : null;
    $("#formTitle").textContent = i ? `Edit ${i.name}` : "Add instance";
    $("#f_reset").classList.toggle("hidden", !i);
    $("#f_name").value = i?.name || ""; $("#f_name").disabled = !!i;
    $("#f_env").value = i?.env || ""; $("#f_host").value = i?.host || "";
    $("#f_port").value = i?.port || 6432; $("#f_user").value = i?.user || "";
    $("#f_pass").value = ""; $("#f_db").value = i?.dbname || "pgbouncer";
    $("#f_enabled").checked = i ? !!i.enabled : true;
    $("#f_admin").checked = i ? !!i.admin : false;
  };

  const body = () => ({
    name: $("#f_name").value.trim(), env: $("#f_env").value.trim(),
    host: $("#f_host").value.trim(), port: +$("#f_port").value || 6432,
    user: $("#f_user").value.trim(), password: $("#f_pass").value,
    dbname: $("#f_db").value.trim() || "pgbouncer",
    enabled: $("#f_enabled").checked, admin: $("#f_admin").checked,
  });

  $("#f_save").onclick = async () => {
    const b = body();
    if (!b.name || !b.host) return toast("name and host are required", "err");
    try {
      if (editing) await api(`/api/instances/${encodeURIComponent(editing)}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(b) });
      else await api("/api/instances", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(b) });
      toast(editing ? "instance updated" : "instance added — monitoring started", "ok");
      render();
    } catch (e) { toast(e.message, "err"); }
  };
  $("#f_probe").onclick = async () => {
    const out = $("#probeOut");
    out.innerHTML = '<div class="probebox">probing…</div>';
    try {
      const r = await api("/api/probe", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body()) });
      out.innerHTML = `<div class="probebox">${r.ok
        ? `OK  ${r.version}\ncapabilities: ${Object.entries(r.caps || {}).filter(([, v]) => v).map(([k]) => k).join(", ") || "basic"}`
        : "FAILED: " + (r.error || "unknown")}</div>`;
    } catch (e) { out.innerHTML = `<div class="probebox">FAILED: ${esc(e.message)}</div>`; }
  };
  $("#f_reset").onclick = () => fill(null);
  m.querySelectorAll("[data-edit]").forEach((b) => b.onclick = () =>
    fill(inst.find((x) => x.name === b.dataset.edit)));
  m.querySelectorAll("[data-test]").forEach((b) => b.onclick = async () => {
    const i = inst.find((x) => x.name === b.dataset.test);
    fill(i);
    $("#f_probe").click();
  });
  m.querySelectorAll("[data-del]").forEach((b) => b.onclick = () => {
    const name = b.dataset.del;
    showModal(`Delete ${name}`, `<p>Remove <b>${esc(name)}</b> from monitoring? Stored credentials are dropped from the config.</p>`, { confirm: false, label: "Delete" }).then(async (ok) => {
      if (!ok) return;
      try { await api(`/api/instances/${encodeURIComponent(name)}`, { method: "DELETE" }); toast("removed", "ok"); render(); }
      catch (e) { toast(e.message, "err"); }
    });
  });
  $("#t_save").onclick = async () => {
    try {
      await api("/api/thresholds", {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          wait_warn_ms: +$("#t_ww").value, wait_crit_ms: +$("#t_wc").value,
          waiting_clients_warn: +$("#t_cw").value,
          client_util_warn_pct: +$("#t_cu").value, fd_warn_pct: +$("#t_fd").value,
        }),
      });
      toast("thresholds saved", "ok");
    } catch (e) { toast(e.message, "err"); }
  };
}

/* ---------- audit ---------- */
async function pageAudit(m) {
  let entries = [];
  try { entries = (await api("/api/audit")).entries || []; } catch {}
  const rows = entries.map((a) => `<tr>
    <td class="mono">${new Date(a.at).toLocaleTimeString()}</td>
    <td><span class="badge ${a.ok ? "live" : "crit"}">${a.ok ? "OK" : "FAIL"}</span></td>
    <td>${esc(a.actor)}</td><td>${esc(a.instance)}</td>
    <td><b>${esc(a.verb)}</b>${a.database ? " " + esc(a.database) : ""}</td>
    <td class="mono">${esc(a.ok ? (a.result || "") : (a.error || ""))}</td>
  </tr>`).join("");
  m.innerHTML = `<div class="panel"><h2>Operations audit <span class="sub">journaled actions, newest first — also appended to audit.jsonl</span></h2>
    ${rows ? `<div class="tablewrap"><table><thead><tr><th>Time</th><th></th><th>Actor</th><th>Instance</th><th>Action</th><th>Result</th></tr></thead><tbody>${rows}</tbody></table></div>` : '<div class="empty">No audit entries yet.</div>'}
  </div>`;
}

/* ---------- modal ---------- */
let modalResolve = null;
function showModal(title, bodyHtml, opts = {}) {
  $("#modalTitle").textContent = title;
  $("#modalBody").innerHTML = bodyHtml;
  const cw = $("#modalConfirmWrap");
  if (opts.confirm) {
    cw.classList.remove("hidden");
    $("#modalExpect").textContent = opts.expect;
    $("#modalConfirm").value = "";
  } else cw.classList.add("hidden");
  $("#modalOk").textContent = opts.label || "Run";
  $("#modal").classList.remove("hidden");
  (opts.confirm ? $("#modalConfirm") : $("#modalOk")).focus();
  return new Promise((res) => { modalResolve = res; });
}
$("#modalCancel").onclick = () => { $("#modal").classList.add("hidden"); modalResolve?.(false); };
$("#modalOk").onclick = () => {
  const exp = $("#modalExpect").textContent;
  if (!$("#modalConfirmWrap").classList.contains("hidden") &&
      $("#modalConfirm").value.trim().toUpperCase() !== exp.toUpperCase()) {
    toast("confirmation does not match — type " + exp, "err");
    return;
  }
  $("#modal").classList.add("hidden");
  modalResolve?.(true);
};
$("#modalConfirm").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("#modalOk").click();
});
// Track whether the user has typed into the visible form.
document.addEventListener("input", (e) => {
  if (e.target.closest("#main")) S.formDirty = true;
});
document.addEventListener("click", (e) => {
  if (e.target.closest("#f_save,#f_reset,#t_save,nav")) S.formDirty = false;
});
$("#modal").addEventListener("click", (e) => {
  if (e.target.id === "modal") { $("#modal").classList.add("hidden"); modalResolve?.(false); }
});

/* ---------- boot ---------- */
(async function boot() {
  try {
    const a = await api("/api/auth");
    if (a.required && !a.authed) {
      const t = prompt("bouncewarden access token:");
      if (t) await api("/api/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token: t }) });
    }
  } catch { /* continue unauthenticated until server enforces */ }
  route();
  await load();
  connect();
})();
