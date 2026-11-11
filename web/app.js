(() => {
  "use strict";
  const S = { host: "all", period: "current", start: "", end: "", gran: "day", iface: "eth0", tz: "Asia/Singapore", ipView: "ip", factor: "2", hosts: [] };
  let chart = null, dchart = null;
  const $ = (s) => document.querySelector(s);
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  function bytes(n, digits = 2) {
    n = Number(n) || 0;
    const u = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
    let i = 0;
    while (Math.abs(n) >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return { v: i === 0 ? String(Math.round(n)) : n.toFixed(n >= 100 ? 1 : digits), u: u[i] };
  }
  const fmt = (n, d) => { const b = bytes(n, d); return `${b.v} ${b.u}`; };
  const big = (n) => { const b = bytes(n); return `${b.v}<small>${b.u}</small>`; };
  const pct = (a, b) => (b > 0 ? (a * 100) / b : 0);

  const dtf = (opts) => new Intl.DateTimeFormat("zh-CN", { timeZone: S.tz, hour12: false, ...opts });
  const fmtDay = (ts) => dtf({ month: "2-digit", day: "2-digit" }).format(ts * 1000).replace("/", "-");
  const fmtHour = (ts) => dtf({ month: "2-digit", day: "2-digit", hour: "2-digit" }).format(ts * 1000).replace("/", "-").replace(" ", " ") + ":00";
  const fmtMin = (ts) => dtf({ hour: "2-digit", minute: "2-digit" }).format(ts * 1000);
  const fmtTime = (ts) => dtf({ month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(ts * 1000).replace("/", "-");

  function qs(extra = {}) {
    const p = new URLSearchParams({ period: S.period, ...extra });
    if (S.period === "custom") { p.set("start", S.start); p.set("end", S.end); }
    return p.toString();
  }

  async function api(path, opts) {
    const r = await fetch(path, { credentials: "same-origin", ...(opts || {}) });
    if (r.status === 401) { showLogin(); throw new Error("需要登录"); }
    if (!r.ok) { let m = r.statusText; try { m = (await r.json()).error || m; } catch (_) {} throw new Error(m); }
    return r.json();
  }

  function showLogin() { $("#login").classList.remove("hidden"); setTimeout(() => $("#loginToken").focus(), 50); }
  $("#loginForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    $("#loginErr").textContent = "";
    const r = await fetch("/api/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token: $("#loginToken").value }) });
    if (r.ok) { $("#loginToken").value = ""; $("#login").classList.add("hidden"); load(); }
    else { let m = "口令错误"; try { m = (await r.json()).error || m; } catch (_) {} $("#loginErr").textContent = m; }
  });

  function seg(el, attr, onPick) {
    el.addEventListener("click", (e) => {
      const b = e.target.closest("button");
      if (!b || !el.contains(b)) return;
      el.querySelectorAll("button").forEach((x) => x.classList.toggle("on", x === b));
      onPick(b.dataset[attr]);
    });
  }

  // ---------- 总览 ----------
  const MODE = { both: "双向合计", tx: "仅出站", rx: "仅入站", max: "取大" };
  const COLL = { netdev: "网卡", singbox: "sing-box", site: "站点", netbird: "NetBird", xray: "xray" };

  function renderHosts(ov) {
    const hs = ov.hosts;
    S.hosts = hs;
    // 主机切换
    const seg = $("#hostSeg");
    const names = ["all", ...hs.map((h) => h.name)];
    if (seg.dataset.names !== names.join(",")) {
      seg.dataset.names = names.join(",");
      seg.innerHTML = names.map((n) => `<button data-host="${esc(n)}" class="${n === S.host ? "on" : ""}">${n === "all" ? "全部" : esc(n)}</button>`).join("");
    }
    const online = hs.filter((h) => h.online).length;
    const t = ov.totals;
    const p = ov.period;
    $("#hero").innerHTML = `
      <div><div class="k">${({ current: "本期", previous: "上期", last1h: "最近 1 小时" })[p.label] || "区间"}计费流量 · 全部主机</div>
        <div class="v">${big(t.billed)}</div>
        <div class="s"><span>${esc(p.start)} → ${esc(p.end)}</span><span>每机按各自口径与周期</span></div></div>
      <div><div class="k">计费网卡 入站</div><div class="v arrow-rx">${big(t.iface.rx)}</div><div class="s">rx</div></div>
      <div><div class="k">计费网卡 出站</div><div class="v arrow-tx">${big(t.iface.tx)}</div><div class="s">tx</div></div>
      <div><div class="k">在线主机</div><div class="v">${online}<small>/ ${hs.length}</small></div>
        <div class="s"><span>sing-box 载荷 ${fmt(t.singbox.rx + t.singbox.tx)}</span></div></div>`;

    $("#hosts").innerHTML = hs.length ? hs.map((h) => {
      const ifc = h.ifaces[h.billing_iface] || { rx: 0, tx: 0 };
      const q = h.quota > 0 ? pct(h.billed, h.quota) : 0;
      const fq = h.quota > 0 ? pct(h.forecast, h.quota) : 0;
      const qcls = q >= 100 ? "bad" : q >= 80 ? "warn" : "";
      const coll = Object.entries(h.collectors || {}).map(([k, c]) => {
        const st = c.fails > 0 ? (c.last_ok ? "warn" : "bad") : "";
        const tip = c.fails > 0 ? `连续失败 ${c.fails} 次:${c.last_err || ""}` : c.note || "正常";
        return `<span class="chip ${st}" title="${esc(tip)}">${esc(COLL[k] || k)}</span>`;
      }).join("");
      const extra = [];
      if (h.buffered > 0) extra.push(`<span class="chip warn" title="agent 本地缓冲待补发">积压 ${h.buffered}</span>`);
      if (h.dropped > 0) extra.push(`<span class="chip bad" title="缓冲超限丢弃">丢弃 ${h.dropped}</span>`);
      if (h.revoked) extra.push(`<span class="chip bad">已吊销</span>`);
      const status = h.last_seen ? (h.online ? "在线" : `离线 · 最后 ${fmtTime(h.last_seen)}`) : "尚未上报";
      return `<article class="host ${S.host === h.name ? "sel" : ""}" data-host="${esc(h.name)}">
        <div class="top"><div class="name"><span class="dot ${h.last_seen ? (h.online ? "on" : "off") : ""}" title="${esc(status)}"></span>${esc(h.name)}</div>
          <span class="ver">${esc(h.agent_version || "")}</span></div>
        <div class="big">${big(h.billed)}<small>${esc(MODE[h.billing_mode] || h.billing_mode)} · ${esc(h.billing_iface)}</small></div>
        ${h.quota > 0 ? `<div class="bar-q ${qcls}"><i data-w="${Math.min(q, 100).toFixed(1)}"></i>${h.forecast ? `<b data-l="${Math.min(fq, 100).toFixed(1)}" title="预测"></b>` : ""}</div>
          <div class="hint">配额 ${fmt(h.quota)} · 已用 ${q.toFixed(1)}%${h.forecast ? ` · 预测 ${fq.toFixed(0)}%` : ""}</div>` : `<div class="hint noquota">未设配额</div>`}
        <div class="kv">
          <div><div class="k">入 / 出</div><div class="v"><span class="arrow-rx">${fmt(ifc.rx, 1)}</span> / <span class="arrow-tx">${fmt(ifc.tx, 1)}</span></div></div>
          <div><div class="k">今日</div><div class="v">${fmt(h.today, 1)}</div></div>
          <div><div class="k">${h.forecast ? "本期预测" : "sing-box 载荷"}</div><div class="v">${h.forecast ? fmt(h.forecast, 1) : fmt(h.singbox.rx + h.singbox.tx, 1)}</div></div>
          <div><div class="k">周期</div><div class="v">${esc(h.period.start.slice(5))} → ${esc(h.period.end.slice(5))}${h.days_left ? ` · 剩 ${h.days_left.toFixed(1)} 天` : ""}</div></div>
        </div>
        <div class="chips">${coll}${extra.join("")}</div>
      </article>`;
    }).join("") : `<div class="panel empty">还没有主机。用 <code>meterly-server host add NAME</code> 创建并配置 agent。</div>`;
    applyBars($("#hosts"));
  }

  // CSP 禁止内联 style 属性:宽度用 data-* 渲染后经 CSSOM 设置
  function applyBars(root) {
    root.querySelectorAll("[data-w]").forEach((el) => { el.style.width = el.dataset.w + "%"; });
    root.querySelectorAll("[data-l]").forEach((el) => { el.style.left = el.dataset.l + "%"; });
  }

  // ---------- 曲线 ----------
  const effGran = () => (S.period === "last1h" ? "minute" : S.gran);
  const fmtTick = (g) => (g === "day" ? fmtDay : g === "minute" ? fmtMin : fmtHour);

  function drawChart(canvas, old, pts, gran, labels2) {
    const fmtL = fmtTick(gran);
    const labels = pts.map((p) => fmtL(p.ts));
    const ctx = canvas.getContext("2d");
    const h = canvas.parentElement.clientHeight || 280;
    const grad = (c1) => { const g = ctx.createLinearGradient(0, 0, 0, h); g.addColorStop(0, c1 + "55"); g.addColorStop(1, c1 + "00"); return g; };
    const bar = gran === "day";
    const names = labels2 || ["入站 rx", "出站 tx"];
    const ds = [
      { label: names[0], data: pts.map((p) => p.rx), c: "#7cc7f2" },
      { label: names[1], data: pts.map((p) => p.tx), c: "#b39dfa" },
    ].map((x) => bar
      ? { label: x.label, data: x.data, backgroundColor: x.c + "b3", hoverBackgroundColor: x.c, borderRadius: 4, borderSkipped: false, maxBarThickness: 26, categoryPercentage: 0.62, barPercentage: 0.9 }
      : { label: x.label, data: x.data, borderColor: x.c, backgroundColor: grad(x.c), fill: true, tension: 0.35, borderWidth: 1.6, pointRadius: 0, pointHoverRadius: 3 });
    if (old) old.destroy();
    return new Chart(ctx, {
      type: bar ? "bar" : "line",
      data: { labels, datasets: ds },
      options: {
        maintainAspectRatio: false, animation: { duration: 300 }, interaction: { mode: "index", intersect: false },
        plugins: { legend: { display: false }, tooltip: {
          backgroundColor: "#151922", borderColor: "rgba(255,255,255,.12)", borderWidth: 1, padding: 10, titleColor: "#e9ebf0", bodyColor: "#c5cad4",
          callbacks: { label: (c) => ` ${c.dataset.label}  ${fmt(c.parsed.y)}` } } },
        scales: {
          x: { grid: { display: false }, border: { color: "rgba(255,255,255,.08)" }, ticks: { color: "#5d6472", maxRotation: 0, autoSkipPadding: 18, font: { size: 11 } } },
          y: { beginAtZero: true, grid: { color: "rgba(255,255,255,.05)" }, border: { display: false }, ticks: { color: "#5d6472", font: { size: 11 }, callback: (v) => fmt(v, 0), maxTicksLimit: 5 } },
        },
      },
    });
  }

  async function loadSeries() {
    const g = effGran();
    const d = await api(`/api/v1/series?${qs({ host: S.host, gran: g, kind: "iface", dim1: S.iface })}`);
    const pts = d.points || [];
    const fmtL = fmtTick(g);
    const sum = pts.reduce((a, p) => a + p.rx + p.tx, 0);
    const peak = pts.reduce((m, p) => (p.rx + p.tx > m.v ? { v: p.rx + p.tx, ts: p.ts } : m), { v: 0, ts: 0 });
    $("#seriesHint").textContent = `${S.host === "all" ? "全部主机" : S.host} · ${S.iface} · ${g === "minute" ? "最近 1 小时 · 每分钟 · " : ""}合计 ${fmt(sum)}` + (peak.v ? ` · 峰值 ${fmt(peak.v)}(${fmtL(peak.ts)})` : "");
    chart = drawChart($("#seriesChart"), chart, pts, g);
  }

  // ---------- sing-box ----------
  function shareCell(v, total) {
    const p = pct(v, total);
    return `<div class="sbar"><i><b data-w="${Math.min(p, 100).toFixed(1)}"></b></i><span>${p < 0.1 && p > 0 ? "<0.1" : p.toFixed(1)}%</span></div>`;
  }
  const relayTag = (r) => (r ? `<span class="tag relay" title="direct 端口转发:来源 IP 是中转机,不是终端用户">中转</span>` : "");
  const inbName = (s) => esc(String(s).replace(/^[a-z0-9-]+\//, (m) => (m === "direct/" ? "" : m)));

  const flag = (cc) => (cc && /^[A-Z]{2}$/.test(cc) ? String.fromCodePoint(...[...cc].map((c) => 0x1f1a5 + c.charCodeAt(0))) : "");
  function geoCell(g) {
    if (!g) return `<span class="dim">—</span>`;
    if (g.scope) return `<span class="scope">${esc(g.scope)}</span>`;
    if (!g.cc) return `<span class="dim">未知</span>`;
    const place = [g.region, g.city].filter((x) => x && x !== g.country).join(" · ");
    const short = g.city || g.region || "";
    return `<span class="geo" title="${esc([g.country, place].filter(Boolean).join(" · "))}"><span class="flag">${flag(g.cc)}</span>${esc(g.country || g.cc)}${short && short !== g.country ? `<span class="city">${esc(short)}</span>` : ""}</span>`;
  }
  const orgCell = (g) => (g && g.asn ? `<span class="org" title="${esc(g.org)}">${esc(g.org || "")}</span> <span class="asn">AS${g.asn}</span>` : `<span class="dim">—</span>`);

  const SPECIAL = { unattributed: "未归因(轮询间隙)", other: "其他(Top-N 之外)" };
  let lastInb = [];

  async function loadSingbox() {
    const view = S.ipView;
    const reqs = [api(`/api/v1/singbox/inbounds?${qs({ host: S.host })}`)];
    reqs.push(view === "ip" ? api(`/api/v1/singbox/top?${qs({ host: S.host, limit: 100 })}`) : api(`/api/v1/singbox/geo?${qs({ host: S.host, by: view })}`));
    const [inb, top] = await Promise.all(reqs);
    const total = top.total.rx + top.total.tx;
    $("#ipTotal").textContent = `合计 ${fmt(total)}`;
    $("#ipTitle").textContent = view === "ip" ? "来源 IP × 入站" : view === "country" ? "按国家 / 地区" : "按 ASN(运营商)";
    const thead = $("#ipTable thead"), tbody = $("#ipTable tbody");
    if (view === "ip") {
      thead.innerHTML = `<tr><th>#</th><th>来源 IP</th><th>归属地</th><th>运营商 / ASN</th><th>入站</th><th class="num">上传</th><th class="num">下载</th><th class="num">合计</th><th class="share">占比</th></tr>`;
      let rank = 0;
      tbody.innerHTML = top.rows.length ? top.rows.map((r) => {
        const special = SPECIAL[r.special] || "";
        const ibs = r.inbounds || [];
        const first = ibs[0];
        const inbHtml = first ? `<span class="inb">${relayTag(first.relay)}${inbName(first.inbound)}${ibs.length > 1 ? `<span class="more" title="${esc(ibs.map((x) => `${x.inbound} ${fmt(x.rx + x.tx)}`).join("\n"))}">+${ibs.length - 1}</span>` : ""}</span>` : `<span class="dim">—</span>`;
        return `<tr${special ? "" : ` data-k="${esc(r.ip)}"`}><td class="rank">${special ? "" : ++rank}</td><td class="ip ${special ? "special" : ""}">${special ? esc(special) : esc(r.ip)}</td>
          <td>${special ? "" : geoCell(r.geo)}</td><td>${special ? "" : orgCell(r.geo)}</td>
          <td>${inbHtml}</td><td class="num">${fmt(r.rx, 1)}</td><td class="num">${fmt(r.tx, 1)}</td><td class="num">${fmt(r.total, 1)}</td><td>${shareCell(r.total, total)}</td></tr>`;
      }).join("") : `<tr><td colspan="9" class="empty">此区间暂无 sing-box 数据</td></tr>`;
    } else {
      thead.innerHTML = `<tr><th>#</th><th>${view === "country" ? "国家 / 地区" : "ASN"}</th><th class="num">IP 数</th><th>主要 IP</th><th class="num">上传</th><th class="num">下载</th><th class="num">合计</th><th class="share">占比</th></tr>`;
      let rank = 0;
      tbody.innerHTML = top.rows.length ? top.rows.map((g) => {
        const sp = g.special === "unattributed" || g.special === "other";
        const name = view === "country"
          ? (g.cc ? `<span class="geo"><span class="flag">${flag(g.cc)}</span>${esc(g.name)}<span class="asn">${esc(g.cc)}</span></span>` : `<span class="dim">${esc(g.name)}</span>`)
          : (g.asn ? `<span class="org" title="${esc(g.name)}">${esc(g.name)}</span> <span class="asn">AS${g.asn}${g.cc ? " · " + esc(g.cc) : ""}</span>` : `<span class="dim">${esc(g.name)}</span>`);
        const tops = (g.top || []).map((ip) => `<button class="btn link mono" data-ip="${esc(ip)}">${esc(ip)}</button>`).join(" ");
        return `<tr><td class="rank">${sp ? "" : ++rank}</td><td>${name}</td><td class="num">${sp ? "—" : g.ips}</td><td>${tops}</td>
          <td class="num">${fmt(g.rx, 1)}</td><td class="num">${fmt(g.tx, 1)}</td><td class="num">${fmt(g.total, 1)}</td><td>${shareCell(g.total, total)}</td></tr>`;
      }).join("") : `<tr><td colspan="8" class="empty">此区间暂无 sing-box 数据</td></tr>`;
    }
    lastInb = inb.rows;
    const itotal = inb.rows.reduce((a, r) => a + r.total, 0);
    $("#inbTable tbody").innerHTML = inb.rows.length ? inb.rows.map((r, i) => `<tr${r.inbound.startsWith("(") ? "" : ` data-k="${i}"`}><td><span class="inb">${relayTag(r.relay)}${inbName(r.inbound)}</span></td>
      <td class="num">${r.ips}</td><td class="num">${fmt(r.total, 1)}</td><td>${shareCell(r.total, itotal)}</td></tr>`).join("")
      : `<tr><td colspan="4" class="empty">暂无</td></tr>`;
    applyBars($("#ipTable"));
    applyBars($("#inbTable"));
  }

  // ---------- 明细抽屉 ----------
  function openDrawer(title, sub) {
    $("#drawerTitle").textContent = title;
    $("#drawerSub").textContent = sub || "";
    $("#drawerKV").innerHTML = "";
    $("#drawerBody").innerHTML = "";
    $("#drawer").classList.remove("hidden");
  }
  function closeDrawer() { $("#drawer").classList.add("hidden"); if (dchart) { dchart.destroy(); dchart = null; } }
  $("#drawerClose").addEventListener("click", closeDrawer);
  $("#drawer").addEventListener("click", (e) => { if (e.target.id === "drawer") closeDrawer(); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeDrawer(); });
  const kv = (pairs) => pairs.map(([k, v]) => `<div><div class="k">${esc(k)}</div><div class="v">${v}</div></div>`).join("");
  const periodText = (p) => `${p.start} → ${p.end}`;
  const showChart = (on) => { $("#drawerChartBox").classList.toggle("hidden", !on); $("#drawerLegend").classList.toggle("hidden", !on); };

  async function openIP(ip) {
    openDrawer(ip, "加载中…");
    showChart(true);
    const d = await api(`/api/v1/singbox/ip?${qs({ host: S.host, ip })}`);
    const g = d.geo || {};
    $("#drawerSub").textContent = `${S.host === "all" ? "全部主机" : S.host} · ${periodText(d.period)} · ${({ minute: "每分钟", hour: "每小时", day: "每天" })[d.gran]}`;
    $("#drawerKV").innerHTML = kv([
      ["归属地", geoCell(g)],
      ["运营商 / ASN", g.asn ? `${esc(g.org)} · AS${g.asn}` : "—"],
      ["上传", fmt(d.total.rx)], ["下载", fmt(d.total.tx)], ["合计", fmt(d.total.rx + d.total.tx)],
    ]);
    dchart = drawChart($("#drawerChart"), dchart, d.points || [], d.gran, ["上传", "下载"]);
    const tot = d.total.rx + d.total.tx;
    $("#drawerBody").innerHTML = `<h3>入站</h3><table class="tbl"><thead><tr><th>入站</th><th class="num">上传</th><th class="num">下载</th><th class="share">占比</th></tr></thead><tbody>` +
      ((d.inbounds || []).map((x) => `<tr><td><span class="inb">${relayTag(x.relay)}${inbName(x.inbound)}</span></td><td class="num">${fmt(x.rx, 1)}</td><td class="num">${fmt(x.tx, 1)}</td><td>${shareCell(x.rx + x.tx, tot)}</td></tr>`).join("") || `<tr><td colspan="4" class="empty">无</td></tr>`) +
      `</tbody></table><p class="note">曲线来自小时表:某小时该 IP 不在该机 Top-50 时会并入「其他」,曲线可能偏低;合计以排行为准。最近 1 小时视图为分钟明细(保留 25 小时)。</p>`;
    applyBars($("#drawerBody"));
  }

  async function openInbound(r) {
    openDrawer(r.inbound, "加载中…");
    showChart(true);
    const g = effGran() === "day" ? "hour" : effGran();
    const d = await api(`/api/v1/series?${qs({ host: S.host, kind: "singbox", dim2: r.inbound, gran: S.period === "last1h" ? "minute" : g })}`);
    $("#drawerSub").textContent = `${S.host === "all" ? "全部主机" : S.host} · ${periodText(d.period)} · ${S.period === "last1h" ? "每分钟" : "每小时"}${r.relay ? " · 中转入站(来源 IP 是中转机)" : ""}`;
    $("#drawerKV").innerHTML = kv([["来源 IP 数", String(r.ips)], ["上传", fmt(r.rx)], ["下载", fmt(r.tx)], ["合计", fmt(r.total)]]);
    dchart = drawChart($("#drawerChart"), dchart, d.points || [], S.period === "last1h" ? "minute" : "hour", ["上传", "下载"]);
  }

  $("#ipTable").addEventListener("click", (e) => {
    const b = e.target.closest("[data-ip]");
    if (b) { openIP(b.dataset.ip).catch(drawerErr); return; }
    const tr = e.target.closest("tr[data-k]");
    if (tr && S.ipView === "ip") openIP(tr.dataset.k).catch(drawerErr);
  });
  $("#inbTable").addEventListener("click", (e) => {
    const tr = e.target.closest("tr[data-k]");
    if (tr) openInbound(lastInb[Number(tr.dataset.k)]).catch(drawerErr);
  });
  function drawerErr(e) { if (e.message !== "需要登录") $("#drawerSub").textContent = "加载失败:" + e.message; }

  // ---------- 对账 ----------
  async function loadRecon() {
    const d = await api(`/api/v1/reconcile?${qs({ host: S.host, factor: S.factor })}`);
    const rows = d.rows || [];
    $("#recon").innerHTML = rows.length ? rows.map((r) => {
      const T = r.iface_total;
      const w = (v) => (T > 0 ? Math.max(0, Math.min(100, (v * 100) / T)) : 0);
      const un = r.unattributed;
      const siteT = r.site ? r.site.rx + r.site.tx : 0;
      const nbT = r.netbird ? r.netbird.rx + r.netbird.tx : 0;
      const sbW = w(r.singbox_est), siteW = w(siteT), nbW = w(nbT), unW = un > 0 ? w(un) : 0;
      return `<div class="recon-row">
        <div class="recon-top"><span class="h">${esc(r.host)} · ${esc(r.iface)}</span><span class="t">${fmt(T)} · ${esc(periodText(r.period))}</span></div>
        <div class="stack"><i class="c-sb" data-w="${sbW.toFixed(2)}"></i><i class="c-site" data-w="${siteW.toFixed(2)}"></i><i class="c-nb" data-w="${nbW.toFixed(2)}"></i><i class="c-un" data-w="${unW.toFixed(2)}"></i></div>
        <div class="recon-leg">
          <div><div class="k sb">sing-box ×${r.singbox_factor}</div><div class="v">${fmt(r.singbox_est, 1)}</div><div class="s">载荷 ${fmt(r.singbox.rx + r.singbox.tx, 1)}</div></div>
          <div><div class="k site">站点</div><div class="v">${r.site ? fmt(r.site.rx + r.site.tx, 1) : "—"}</div><div class="s">仅下行${r.site && r.site.rx ? " · 含上行" : ""}</div></div>
          <div><div class="k nb">NetBird</div><div class="v">${r.netbird ? fmt(r.netbird.rx + r.netbird.tx, 1) : "—"}</div><div class="s">${r.netbird_iface ? `wt0 参考 ${fmt(r.netbird_iface.rx + r.netbird_iface.tx, 1)}` : "—"}</div></div>
          <div><div class="k un">未归因</div><div class="v ${un < 0 ? "neg" : ""}">${un < 0 ? "−" + fmt(-un, 1) : fmt(un, 1)}</div><div class="s">${T > 0 ? r.unattributed_pct.toFixed(1) + "%" : "—"}</div></div>
        </div></div>`;
    }).join("") : `<div class="empty">暂无主机</div>`;
    applyBars($("#recon"));
  }
  seg($("#factorSeg"), "factor", (f) => { S.factor = f; loadRecon().catch(() => {}); });

  // ---------- 服务商数字 ----------
  const pctCls = (p) => (Math.abs(p) <= 1 ? "ok" : Math.abs(p) <= 3 ? "warn" : "bad");
  const sgn = (n) => (n < 0 ? "−" + fmt(-n, 2) : "+" + fmt(n, 2));
  let provRows = [], provEdit = null;
  async function loadProvider() {
    const d = await api(`/api/v1/provider?${qs({ host: S.host })}`);
    provRows = d.rows || [];
    $("#provTable tbody").innerHTML = provRows.length ? provRows.map((r, i) => {
      const rd = r.reading;
      return `<tr><td>${esc(r.host)}<div class="asn">${esc(MODE[r.billing_mode] || r.billing_mode)} · ${esc(r.iface)}</div></td>
        <td class="mono">${esc(r.cycle_start.slice(5))} → ${esc(r.cycle_end.slice(5))}</td>
        <td class="num">${fmt(r.ours, 2)}</td>
        <td class="num">${rd ? fmt(rd.bytes, 2) : `<span class="dim">未录入</span>`}</td>
        <td class="num">${rd ? sgn(r.diff) : ""}</td>
        <td class="num">${rd ? `<span class="dpct ${pctCls(r.pct)}">${r.pct > 0 ? "+" : ""}${r.pct.toFixed(2)}%</span>` : ""}</td>
        <td class="num"><button class="btn link" data-edit="${i}">${rd ? "修改" : "录入"}</button>${rd ? `<button class="btn link bad" data-del="${i}">删除</button>` : ""}
          <button class="btn link" data-vn="${i}">vnstat</button></td></tr>` +
        (r.history || []).filter((h) => h.cycle_start !== r.cycle_start).slice(0, 3).map((h) => `<tr class="hist"><td></td><td class="mono dim">${esc(h.cycle_start.slice(5))} → ${esc(h.cycle_end.slice(5))}</td>
          <td class="num dim">${fmt(h.ours, 2)}</td><td class="num dim">${fmt(h.bytes, 2)}</td><td class="num dim">${sgn(h.diff)}</td>
          <td class="num"><span class="dpct ${pctCls(h.pct)}">${h.pct > 0 ? "+" : ""}${h.pct.toFixed(2)}%</span></td><td></td></tr>`).join("");
    }).join("") : `<tr><td colspan="7" class="empty">暂无主机</td></tr>`;
  }
  $("#provTable").addEventListener("click", async (e) => {
    const ed = e.target.closest("[data-edit]"), del = e.target.closest("[data-del]"), vn = e.target.closest("[data-vn]");
    if (ed) {
      const r = provRows[Number(ed.dataset.edit)];
      provEdit = r;
      $("#provFormTitle").textContent = `${r.host} · 周期 ${r.cycle_start} → ${r.cycle_end} · 按「${MODE[r.billing_mode] || r.billing_mode}」口径`;
      $("#provValue").value = r.reading ? String(r.reading.bytes) : "";
      $("#provNote").value = r.reading ? r.reading.note : "";
      $("#provErr").textContent = "";
      $("#provForm").classList.remove("hidden");
      $("#provValue").focus();
    } else if (del) {
      const r = provRows[Number(del.dataset.del)];
      if (!confirm(`删除 ${r.host} ${r.cycle_start} 周期的服务商数字?`)) return;
      await api(`/api/v1/provider?host=${encodeURIComponent(r.host)}&cycle_start=${encodeURIComponent(r.cycle_start)}`, { method: "DELETE" });
      loadProvider();
    } else if (vn) {
      vnHost = provRows[Number(vn.dataset.vn)].host;
      $("#vnstatFile").click();
    }
  });
  $("#provCancel").addEventListener("click", () => $("#provForm").classList.add("hidden"));
  $("#provForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!provEdit) return;
    try {
      await api("/api/v1/provider", { method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ host: provEdit.host, cycle_start: provEdit.cycle_start, value: $("#provValue").value, note: $("#provNote").value }) });
      $("#provForm").classList.add("hidden");
      loadProvider();
    } catch (err) { $("#provErr").textContent = err.message; }
  });

  // ---------- vnstat 自检 ----------
  let vnHost = "";
  $("#vnstatBtn").addEventListener("click", () => {
    vnHost = S.host !== "all" ? S.host : (S.hosts.length === 1 ? S.hosts[0].name : "");
    if (!vnHost) { alert("请先在顶部选择一台主机,或在表格行内点「vnstat」"); return; }
    $("#vnstatFile").click();
  });
  $("#vnstatFile").addEventListener("change", async (e) => {
    const f = e.target.files[0];
    e.target.value = "";
    if (!f || !vnHost) return;
    openDrawer(`vnstat 自检 · ${vnHost}`, `${f.name} · 比对中…`);
    showChart(false);
    try {
      const text = await f.text();
      const d = await api(`/api/v1/vnstat/check?host=${encodeURIComponent(vnHost)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: text });
      $("#drawerSub").textContent = `${f.name} · 时区 ${d.tz} · 只比较已结束、双方都有数据的小时`;
      $("#drawerBody").innerHTML = (d.results || []).map((c) => `
        <div class="drawer-kv">${kv([["网卡", esc(c.iface)], ["比对小时", String(c.total.hours)], ["总偏差", `<span class="dpct ${pctCls(c.total.pct)}">${c.total.pct > 0 ? "+" : ""}${c.total.pct.toFixed(2)}%</span>`],
          ["单日最大偏差", c.max_abs_pct.toFixed(2) + "%"], ["仅 vnstat 有", c.only_vnstat_hours + " 小时"], ["仅本系统有", c.only_ours_hours + " 小时"]])}</div>
        <table class="tbl"><thead><tr><th>日期</th><th class="num">小时</th><th class="num">vnstat</th><th class="num">本系统</th><th class="num">差值</th><th class="num">偏差</th></tr></thead><tbody>
        ${[...c.days, c.total].map((x) => `<tr><td class="mono">${esc(x.day)}</td><td class="num">${x.hours}</td><td class="num">${fmt(x.vnstat.rx + x.vnstat.tx, 2)}</td><td class="num">${fmt(x.ours.rx + x.ours.tx, 2)}</td>
          <td class="num">${sgn(x.diff)}</td><td class="num"><span class="dpct ${pctCls(x.pct)}">${x.pct > 0 ? "+" : ""}${x.pct.toFixed(2)}%</span></td></tr>`).join("")}
        </tbody></table>`).join("") + `<p class="note">vnstat 与本系统同源 /proc/net/dev,正常应几乎一致(≤ 1%);偏差大说明采集有 bug 或有断档。真正的计费准确性以「服务商数字比对」为准。</p>`;
    } catch (err) { drawerErr(err); }
  });

  // ---------- 站点 / NetBird / xray / 采集器 ----------
  async function loadSites() {
    const d = await api(`/api/v1/sites?${qs({ host: S.host })}`);
    const rows = d.sites || [];
    const tot = rows.reduce((a, r) => a + r.rx + r.tx, 0);
    const req = rows.reduce((a, r) => a + r.requests, 0);
    $("#siteTotal").textContent = rows.length ? `${rows.length} 站 · ${fmt(tot)} · ${req} 请求` : "暂无数据";
    $("#siteTable tbody").innerHTML = rows.length ? rows.map((r) => {
      const st = [["2xx", r.s2xx], ["3xx", r.s3xx], ["4xx", r.s4xx], ["5xx", r.s5xx], ["其它", r.sother]].filter((x) => x[1] > 0).map((x) => `${x[0]} ${x[1]}`).join(" · ") || "—";
      return `<tr data-site="${esc(r.site)}"><td>${esc(r.site)}</td><td class="num">${r.requests || 0}</td><td class="num">${fmt(r.tx, 1)}</td><td class="num">${r.rx ? fmt(r.rx, 1) : "—"}</td><td class="dim">${esc(st)}</td></tr>`;
    }).join("") : `<tr><td colspan="5" class="empty">本周期暂无站点日志</td></tr>`;
  }
  $("#siteTable").addEventListener("click", (e) => {
    const tr = e.target.closest("tr[data-site]");
    if (tr) openSite(tr.dataset.site).catch(drawerErr);
  });
  async function openSite(site) {
    openDrawer(site, "加载中…");
    showChart(true);
    const [d, ser] = await Promise.all([
      api(`/api/v1/sites/ips?${qs({ host: S.host, site })}`),
      api(`/api/v1/series?${qs({ host: S.host, kind: "site", dim1: site, gran: S.period === "last1h" ? "minute" : (effGran() === "day" ? "hour" : effGran()) })}`),
    ]);
    const tot = (d.total && d.total.rx + d.total.tx) || 0;
    $("#drawerSub").textContent = `${S.host === "all" ? "全部主机" : S.host} · 按请求结束时刻归因`;
    $("#drawerKV").innerHTML = kv([["下行", fmt((d.total || {}).tx || 0)], ["上行", (d.total && d.total.rx) ? fmt(d.total.rx) : "—"], ["合计", fmt(tot)]]);
    dchart = drawChart($("#drawerChart"), dchart, ser.points || [], S.period === "last1h" ? "minute" : (effGran() === "day" ? "hour" : effGran()), ["上行", "下行"]);
    $("#drawerBody").innerHTML = `<h3>客户端 IP</h3><table class="tbl"><thead><tr><th>IP</th><th class="num">下行</th><th class="num">上行</th><th class="share">占比</th></tr></thead><tbody>` +
      ((d.rows || []).map((x) => `<tr><td class="ip ${x.special ? "special" : ""}">${esc(x.special === "other" ? "其他" : x.ip)}</td><td class="num">${fmt(x.tx, 1)}</td><td class="num">${x.rx ? fmt(x.rx, 1) : "—"}</td><td>${shareCell(x.rx + x.tx, tot)}</td></tr>`).join("") || `<tr><td colspan="4" class="empty">无</td></tr>`) +
      `</tbody></table><p class="note">长连接(如 Emby 播放)的字节全部记在请求结束的那一分钟/小时。per-IP 每小时只留 Top-50,其余并入「其他」。</p>`;
    applyBars($("#drawerBody"));
  }

  async function loadNetbird() {
    const d = await api(`/api/v1/netbird?${qs({ host: S.host })}`);
    const rows = d.peers || [];
    const tot = (d.total && d.total.rx + d.total.tx) || 0;
    $("#nbTotal").textContent = rows.length ? `${rows.length} 个对等端 · ${fmt(tot)}` : "暂无数据";
    $("#nbTable tbody").innerHTML = rows.length ? rows.map((r) => `<tr><td>${esc(r.key)}</td><td class="mono dim">${esc(r.dim2 || "—")}</td>
      <td class="num">${fmt(r.rx, 1)}</td><td class="num">${fmt(r.tx, 1)}</td><td>${shareCell(r.total, tot)}</td></tr>`).join("") : `<tr><td colspan="5" class="empty">本周期暂无 NetBird 数据</td></tr>`;
    applyBars($("#nbTable"));
  }

  async function loadXray() {
    const d = await api(`/api/v1/xray?${qs({ host: S.host })}`);
    const inb = d.inbounds || [], users = d.users || [];
    const empty = !inb.length && !users.length;
    $("#xraySec").classList.toggle("hidden", empty);
    if (empty) return;
    const tot = (d.inbound_total && d.inbound_total.rx + d.inbound_total.tx) || 0;
    $("#xrayTotal").textContent = fmt(tot);
    const rows = (list, total) => list.length ? list.map((r) => `<tr><td>${esc(r.key)}</td><td class="num">${fmt(r.rx, 1)}</td><td class="num">${fmt(r.tx, 1)}</td><td>${shareCell(r.total, total)}</td></tr>`).join("") : `<tr><td colspan="4" class="empty">无</td></tr>`;
    $("#xrayInbTable tbody").innerHTML = rows(inb, tot);
    $("#xrayUserTable tbody").innerHTML = rows(users, (d.user_total && d.user_total.rx + d.user_total.tx) || 0);
    applyBars($("#xraySec"));
  }

  const COLST = { enabled: "正常", disabled: "未启用", error: "失败" };
  async function loadCollectors() {
    const d = await api("/api/v1/collectors");
    const hs = d.hosts || [];
    $("#collWrap").innerHTML = hs.length ? `<table class="tbl" id="collTable"><thead><tr><th>主机</th><th>版本 / 最近上报</th><th>采集器</th></tr></thead><tbody>` +
      hs.map((h) => {
        const seen = h.last_seen ? `${h.online ? "在线" : "离线"} · ${fmtTime(h.last_seen)}` : "尚未上报";
        const chips = (h.collectors || []).map((c) => {
          const tip = c.status === "error" ? `连续失败 ${c.fails} 次${c.last_err ? ":" + c.last_err : ""}` : (c.note || COLST[c.status] || c.status);
          const cls = c.status === "error" ? "bad" : c.status === "enabled" ? "" : "plain";
          return `<span class="chip ${cls}" title="${esc(tip)}">${esc(c.label)} · ${esc(COLST[c.status] || c.status)}</span>`;
        }).join("");
        return `<tr><td><b>${esc(h.name)}</b>${h.hostname && h.hostname !== h.name ? `<div class="asn">${esc(h.hostname)}</div>` : ""}</td>
          <td><span class="mono">${esc(h.agent_version || "—")}</span><div class="asn">${esc(seen)}</div></td>
          <td><div class="chips" style="margin:0">${chips}</div></td></tr>`;
      }).join("") + `</tbody></table>` : `<div class="empty">暂无主机</div>`;
  }
  $("#enrollBtn").addEventListener("click", () => { $("#enrollForm").classList.remove("hidden"); $("#enrollCode").classList.add("hidden"); $("#enrollErr").textContent = ""; $("#enrollHost").focus(); });
  $("#enrollCancel").addEventListener("click", () => $("#enrollForm").classList.add("hidden"));
  $("#enrollForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    $("#enrollErr").textContent = "";
    try {
      const d = await api("/api/v1/enroll", { method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ host_name: $("#enrollHost").value.trim(), ttl_hours: 24, max_uses: 1, reissue: $("#enrollReissue").checked }) });
      $("#enrollCode").classList.remove("hidden");
      $("#enrollCode").innerHTML = `注册码(只显示一次,请立刻复制到安装命令):<br><code style="font-size:13px;word-break:break-all">${esc(d.code)}</code>`;
    } catch (err) { $("#enrollErr").textContent = err.message; }
  });

  async function loadGeoStatus() {
    try {
      const g = await api("/api/v1/geo/status");
      const d = (ts) => (ts ? dtf({ year: "numeric", month: "2-digit" }).format(ts * 1000) : "");
      $("#geoStatus").textContent = g.city.loaded || g.asn.loaded
        ? ` · 库版本 City ${d(g.city.build_time) || "未加载"} / ASN ${d(g.asn.build_time) || "未加载"}`
        : ` · 归属地库未加载${g.last_err ? "(" + g.last_err + ")" : ""}`;
    } catch (_) {}
  }

  async function load() {
    try {
      const me = await api("/api/v1/me");
      S.tz = me.tz || S.tz;
      $("#tzName").textContent = S.tz;
      loadGeoStatus();
      const ov = await api(`/api/v1/overview?${qs()}`);
      $("#clock").textContent = `${dtf({ year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(ov.now * 1000)} · ${S.tz} · 计费周期默认每月 ${ov.period.reset_day} 日`;
      renderHosts(ov);
      await Promise.all([loadSeries(), loadSingbox(), loadRecon(), loadSites(), loadNetbird(), loadXray(), loadCollectors(), loadProvider()]);
    } catch (e) {
      if (e.message !== "需要登录") console.error(e);
    }
  }

  seg($("#hostSeg"), "host", (h) => { S.host = h; load(); });
  $("#hosts").addEventListener("click", (e) => {
    const c = e.target.closest(".host");
    if (!c) return;
    S.host = S.host === c.dataset.host ? "all" : c.dataset.host;
    $("#hostSeg").querySelectorAll("button").forEach((b) => b.classList.toggle("on", b.dataset.host === S.host));
    load();
  });
  function syncGran() {
    const l1 = S.period === "last1h";
    $("#granSeg").querySelectorAll("button").forEach((b) => {
      const isMin = b.dataset.gran === "minute";
      b.classList.toggle("hidden", l1 ? !isMin : isMin);
      b.classList.toggle("on", l1 ? isMin : b.dataset.gran === S.gran);
    });
  }
  seg($("#periodSeg"), "period", (p) => {
    $("#customBox").classList.toggle("hidden", p !== "custom");
    if (p === "custom") return;
    S.period = p; syncGran(); load();
  });
  $("#applyCustom").addEventListener("click", () => {
    S.start = $("#startDate").value; S.end = $("#endDate").value;
    if (!S.start || !S.end) return;
    S.period = "custom"; syncGran(); load();
  });
  seg($("#granSeg"), "gran", (g) => { if (g !== "minute") S.gran = g; loadSeries().catch(() => {}); });
  seg($("#ipView"), "view", (v) => { S.ipView = v; loadSingbox().catch(() => {}); });
  seg($("#ifaceSeg"), "iface", (i) => { S.iface = i; loadSeries().catch(() => {}); });

  load();
  setInterval(() => { if (!document.hidden) load(); }, 60000);
})();
