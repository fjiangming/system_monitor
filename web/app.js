"use strict";

const $ = (s) => document.querySelector(s);
const esc = (v) =>
  String(v ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const icon = (name) =>
  `<i data-lucide="${name}" class="icon" aria-hidden="true"></i>`;
const button = (action, name, text = "", extra = "") =>
  `<button type="button" data-action="${action}" ${extra} ${text ? "" : `class="icon-button" title="${esc(name)}" aria-label="${esc(name)}"`}>${icon(text ? name : { refresh: "refresh-cw", close: "x", theme: "moon", logout: "log-out", logs: "file-text", restart: "rotate-cw", edit: "pencil", remove: "trash-2" }[action] || name)}${esc(text)}</button>`;
const clone = (v) => structuredClone(v);
const get = (o, p) => p.split(".").reduce((x, k) => x?.[k], o);
const set = (o, p, v) => {
  const keys = p.split(".");
  const last = keys.pop();
  let x = o;
  for (const k of keys) x = x[k];
  x[last] = v;
};
const app = {
  view: "monitor",
  category: "servers",
  state: { servers: [], alerts: [], events: [], jobs: [] },
  query: "",
  serverFilter: "",
  detail: "",
  csrf: "",
  user: null,
  setup: false,
  proxyPeer: "",
  proxyNeedsTrust: false,
  settings: null,
  draft: null,
  server: null,
  serverID: "",
  serverRevision: 0,
  globalDirty: false,
  serverDirty: false,
  secrets: {},
  clearSecrets: new Set(),
  notice: "",
  noticeError: false,
  theme: localStorage.getItem("monitor-theme") || "system",
};
const categories = [
  ["servers", "server", "服务器与探针"],
  ["targets", "container", "监控对象"],
  ["network", "network", "流量监控"],
  ["alerts", "bell", "告警规则"],
  ["backup", "database-backup", "备份检查"],
  ["notifications", "send", "通知渠道"],
  ["access", "shield-check", "账号与权限"],
  ["service", "settings-2", "服务设置"],
  ["storage", "hard-drive", "数据与保留"],
];
const stateLabels = {
  running: "运行中",
  exited: "已停止",
  dead: "已退出",
  restarting: "重启中",
  paused: "已暂停",
  created: "未启动",
  healthy: "健康",
  unhealthy: "不健康",
  starting: "健康检查中",
  none: "无健康检查",
  queued: "排队中",
  leased: "执行中",
  success: "检查通过",
  failed: "失败",
  unchecked: "已启动 · 未验健康",
  unknown: "结果未确认",
  cancelled: "已取消",
  downloading: "下载并校验中",
  active: "运行中",
  inactive: "未运行",
  activating: "启动中",
};
function badge(value, kind) {
  return `<span class="badge ${kind || (["running", "healthy", "success", "active"].includes(value) ? "good" : ["exited", "unhealthy", "failed", "dead"].includes(value) ? "bad" : ["restarting", "starting", "unknown", "unchecked"].includes(value) ? "warn" : "")}">${esc(stateLabels[value] || value)}</span>`;
}
function time(at) {
  if (!at) return "尚无记录";
  try {
    return new Intl.DateTimeFormat("zh-CN", {
      timeZone: app.draft?.service.timezone || "Asia/Shanghai",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    }).format(at * 1000);
  } catch {
    return new Date(at * 1000).toLocaleString();
  }
}
function bytes(n) {
  if (n == null) return "—";
  if (n >= 2 ** 30) return (n / 2 ** 30).toFixed(1) + " GiB";
  if (n >= 2 ** 20) return (n / 2 ** 20).toFixed(0) + " MiB";
  return n + " B";
}
function mbps(rate) {
  return rate == null ? "—" : (rate * 8 / 1e6).toFixed(2) + " Mbps";
}
function networkTotal(s) {
  const interfaces = s.snapshot.network || [];
  if (!fresh(s) || !interfaces.length || interfaces.some((n) => !n.ready))
    return null;
  return interfaces.reduce(
    (total, n) => ({
      receive: total.receive + n.receiveBytesPerSecond,
      transmit: total.transmit + n.transmitBytesPerSecond,
      receivePPS: total.receivePPS + n.receivePacketsPerSecond,
      transmitPPS: total.transmitPPS + n.transmitPacketsPerSecond,
    }),
    { receive: 0, transmit: 0, receivePPS: 0, transmitPPS: 0 },
  );
}
function networkStrip(s) {
  const n = networkTotal(s);
  return `<div class="network-strip"><span>${icon("arrow-down")}入站 ${mbps(n?.receive)}</span><span>${icon("arrow-up")}出站 ${mbps(n?.transmit)}</span></div>`;
}
function automaticNetworkStatus(s) {
  if (!s?.config.enabled) return badge("监测已暂停");
  if (!s?.config.network.automatic) return badge("自动识别已关闭");
  if (!fresh(s) || !networkTotal(s)) return badge("暂不可判断", "warn");
  const status = s.networkStatus || {};
  if (!status.state || status.at < s.sampleAt) return badge("分析中");
  if (status.state === "learning")
    return badge("学习中 · " + status.progress + "%", "info");
  if (status.state === "elevated") return badge("流量偏高", "warn");
  return status.state === "normal" ? badge("自动监测中", "good") : badge("暂不可判断", "warn");
}
function networkView(s) {
  const total = networkTotal(s);
  const interfaces = s.snapshot.network || [];
  const rows = interfaces.map((n) => {
    const ready = fresh(s) && n.ready;
    return `<tr><td class="mono">${esc(n.name)}</td>
      <td>${ready ? mbps(n.receiveBytesPerSecond) + " / " + mbps(n.transmitBytesPerSecond) : "速率未知"}</td>
      <td>${ready ? n.receiveDroppedPerSecond.toFixed(1) + " / " + n.transmitDroppedPerSecond.toFixed(1) : "—"}</td>
      <td>${ready ? n.receiveErrorsPerSecond.toFixed(1) + " / " + n.transmitErrorsPerSecond.toFixed(1) : "—"}</td></tr>`;
  }).join("");
  return `<section class="section" aria-label="流量监控">
    <div class="section-title"><h2>网络流量</h2><span class="small muted">${esc(interfaces.map((n) => n.name).join(" · ") || "尚无网卡采样")}</span></div>
    <div class="health-strip">${automaticNetworkStatus(s)}${s.networkStatus?.progress === 100 ? `<span>平时入站 ${mbps(s.networkStatus.baseline.receive)} · 出站 ${mbps(s.networkStatus.baseline.transmit)}</span>` : ""}</div>
    <div class="detail-grid network-summary">
      <div><span class="small muted">入站</span><strong>${mbps(total?.receive)}</strong></div>
      <div><span class="small muted">出站</span><strong>${mbps(total?.transmit)}</strong></div>
      <div><span class="small muted">入站包速率</span><strong>${total ? total.receivePPS.toFixed(0) + " 包/秒" : "—"}</strong></div>
      <div><span class="small muted">出站包速率</span><strong>${total ? total.transmitPPS.toFixed(0) + " 包/秒" : "—"}</strong></div>
    </div>
    <div class="network-legend small muted"><span class="receive">入站</span><span class="transmit">出站</span><span id="network-scale"></span></div>
    <div class="trend network-trend" id="network-trend" aria-label="最近入站与出站流量趋势"></div>
    ${interfaces.length ? `<div class="table-scroll"><table class="network-table"><thead><tr><th>网卡</th><th>入站 / 出站</th><th>丢包（收 / 发，包/秒）</th><th>错误（收 / 发，包/秒）</th></tr></thead><tbody>${rows}</tbody></table></div>` : '<p class="sub">尚无流量采样</p>'}
  </section>`;
}
function agentVersionLabel(v, revision) {
  return (v || "未知版本") + (revision ? " · " + revision.slice(0, 12) : "");
}
function agentUpdateBadge(state) {
  const labels = { queued: "等待领取", downloading: "下载并校验中", restarting: "等待新版采样", success: "更新成功", failed: "更新失败", unknown: "结果未确认", cancelled: "已取消" };
  return badge(labels[state] || state, state === "success" ? "good" : ["failed", "unknown"].includes(state) ? "warn" : "");
}
function agentUpdateContent(s) {
  if (!s) return '<p class="sub">等待探针接入</p>';
  const u = s.agentUpdate || {};
  const latest = u.latest;
  return `<div class="agent-version-grid"><div><span class="small muted">当前探针</span><p class="mono">${esc(agentVersionLabel(s.snapshot.agentVersion, s.snapshot.agent?.revision))}</p></div><div><span class="small muted">面板提供版本</span><p class="mono">${u.target ? esc(agentVersionLabel(u.target.version, u.target.revision)) : "—"}</p></div></div>${latest ? `<div class="health-strip">${agentUpdateBadge(latest.state)}<span class="small">${esc(latest.result || "等待探针领取更新任务")}</span></div>` : ""}<p class="small muted">${esc(u.reason)}</p><div class="form-actions">${admin() ? button("agent-update", "download", "更新探针", `data-id="${esc(s.id)}" ${u.available ? "" : "disabled"} title="${esc(u.reason || "更新到面板提供的版本")}"`) : ""}${latest ? button("agent-update-result", "clipboard-list", "更新记录", `data-update="${esc(latest.id)}"`) : ""}</div>`;
}
function agentUpdateSection(s) {
  return `<section class="section" aria-label="探针更新"><h2>探针版本</h2>${agentUpdateContent(s)}</section>`;
}
function age(at) {
  if (!at) return "尚未接入";
  const s = Math.max(0, Math.round(Date.now() / 1000 - at));
  return s < 60
    ? s + " 秒前"
    : s < 3600
      ? Math.floor(s / 60) + " 分钟前"
      : Math.floor(s / 3600) + " 小时前";
}
function admin() {
  return app.user?.role === "admin";
}
function icons() {
  window.lucide?.createIcons({ attrs: { "stroke-width": 1.7 } });
}
function theme() {
  document.documentElement.style.colorScheme =
    app.theme === "system" ? "light dark" : app.theme;
}
theme();
async function api(path, body) {
  const r = await fetch("/api/" + path, {
    method: body === undefined ? "GET" : "POST",
    headers:
      body === undefined
        ? {}
        : { "Content-Type": "application/json", "X-CSRF-Token": app.csrf },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  let data;
  try {
    data = await r.json();
  } catch {
    throw Error("服务暂时不可用，请检查连接");
  }
  if (!r.ok) {
    if (r.status === 401 && !["login", "setup"].includes(path)) {
      app.user = null;
      render();
    }
    throw Error(data.error || "请求失败");
  }
  return data;
}
function message(text, error = false) {
  app.notice = text;
  app.noticeError = error;
  const el = $("#notice");
  if (el) {
    el.className = "notice" + (error ? " error" : "");
    el.textContent = text;
  } else render();
}
async function bootstrap() {
  const b = await api("bootstrap");
  app.user = b.user?.id ? b.user : null;
  app.setup = b.setup;
  app.proxyPeer = b.proxyPeer || "";
  app.proxyNeedsTrust = b.proxyNeedsTrust || false;
  app.csrf = b.csrf;
  app.siteName = b.siteName;
  app.version = b.version;
  if (app.user) {
    await loadState();
    if (admin()) await loadSettings();
  }
  render();
}
async function loadSettings() {
  app.settings = await api("config");
  app.draft = clone(app.settings.config);
  app.globalDirty = false;
  app.secrets = {};
  app.clearSecrets.clear();
  if (
    !app.server ||
    (app.serverID && !app.state.servers.some((x) => x.id === app.serverID))
  ) {
    selectServer(app.state.servers[0]?.id || "");
  }
}
async function loadState() {
  app.state = await api("state");
}
function selectServer(id) {
  const v = app.state.servers.find((x) => x.id === id);
  app.serverID = v?.id || "";
  app.serverRevision = v?.revision || 0;
  app.server = clone(v?.config || app.settings?.defaultServer || {});
  app.serverDirty = false;
}
function render() {
  const focus = document.activeElement?.id;
  const selection = document.activeElement?.selectionStart;
  if (!app.user) {
    renderAuth();
    return;
  }
  $("#app").innerHTML =
    `<header><div class="brand"><span class="logo">${icon("activity")}</span><span>${esc(app.siteName)}</span></div><div class="header-right"><span class="account">${esc(app.user.username)} · ${admin() ? "管理员" : "只读"}</span>${button("theme", "切换主题")}${button("logout", "退出登录")}</div></header><nav aria-label="主导航">${[["monitor", "activity", "运行监控"], ["events", "list-checks", "事件与操作"], ...(admin() ? [["settings", "settings-2", "系统设置"]] : [])].map(([id, i, n]) => `<button data-action="nav" data-view="${id}" aria-current="${app.view === id ? "page" : "false"}">${icon(i)}${n}</button>`).join("")}</nav><div id="notice" class="notice ${app.noticeError ? "error" : ""}" role="status" aria-live="polite">${esc(app.notice)}</div><main id="main">${app.view === "settings" ? settingsView() : app.view === "events" ? eventsView() : app.detail ? detailView() : monitorView()}</main>`;
  icons();
  if (focus) {
    const el = document.getElementById(focus);
    if (el) {
      el.focus({ preventScroll: true });
      if (
        typeof selection === "number" &&
        el.setSelectionRange &&
        ["text", "search", "url"].includes(el.type)
      )
        el.setSelectionRange(selection, selection);
    }
  }
  if (app.detail && app.view === "monitor") loadTrend();
}
function renderAuth() {
  const proxyFields = app.setup
    ? `<details class="auth-proxy" ${app.proxyNeedsTrust ? "open" : ""}><summary>反向代理设置</summary><label><span>对外访问地址</span><input name="publicURL" type="url" value="${esc(location.origin)}" autocomplete="url"></label><label><span>受信任代理 IP / CIDR</span><textarea name="trustedProxies" rows="2" placeholder="${esc(app.proxyPeer)}">${esc(app.proxyNeedsTrust ? app.proxyPeer : "")}</textarea></label></details>`
    : "";
  $("#app").innerHTML =
    `<main class="auth"><div class="brand"><span class="logo">${icon("activity")}</span>${esc(app.siteName || "轻巡")}</div><h1>${app.setup ? "创建管理员" : "登录监控台"}</h1><p class="sub">${app.setup ? "首次初始化" : "系统运行与容器状态"}</p><div id="notice" class="notice ${app.noticeError ? "error" : ""}" role="status" aria-live="polite">${esc(app.notice)}</div><form id="auth-form">${app.setup ? '<label><span>初始化码</span><input name="code" type="password" required autocomplete="off"></label>' : ""}<label><span>用户名</span><input name="username" required minlength="3" maxlength="64" autocomplete="username"></label><label><span>密码${app.setup ? " · 至少 12 字节" : ""}</span><input name="password" type="password" required ${app.setup ? 'minlength="12"' : ""} maxlength="256" autocomplete="${app.setup ? "new-password" : "current-password"}"></label>${proxyFields}<button class="primary" type="submit">${icon(app.setup ? "shield-check" : "log-in")}${app.setup ? "完成初始化" : "登录"}</button></form></main>`;
  icons();
}
function metric(name, value, display) {
  return `<div class="metric"><div class="value"><span class="muted">${name}</span><span>${esc(display || (value == null ? "—" : value.toFixed(1) + "%"))}</span></div><div class="bar ${value > 90 ? "bad" : value > 80 ? "warn" : ""}" style="--fill:${Math.min(100, Math.max(0, value || 0))}%"><span></span></div></div>`;
}
function fresh(s) {
  return (
    s.online &&
    s.sampleAt > 0 &&
    Date.now() / 1000 - s.sampleAt < s.config.heartbeatTimeout
  );
}
function monitorView() {
  const servers = app.state.servers.filter((s) => s.config.enabled);
  const online = servers.filter((s) => s.online).length;
  const containers = servers.reduce(
    (n, s) => n + (s.snapshot.containers?.length || 0),
    0,
  );
  const alerts = app.state.alerts;
  return `<div class="heading"><div><h1>运行监控</h1><p class="sub">${esc(time(app.state.now))} · ${servers.length} 台服务器</p></div><div class="controls"><input id="server-query" class="filter" type="search" aria-label="筛选服务器" placeholder="搜索服务器、区域" value="${esc(app.query)}">${button("refresh", "刷新状态")}${admin() ? button("add-server", "plus", "添加服务器") : ""}</div></div><div class="stats"><div class="stat"><strong>${online}<span> / ${servers.length}</span></strong><span>探针在线</span></div><div class="stat"><strong>${containers}</strong><span>已发现容器</span></div><div class="stat"><strong>${alerts.length}</strong><span>当前告警</span></div><div class="stat"><strong>${app.state.jobs.filter((j) => ["queued", "leased"].includes(j.state)).length}</strong><span>执行中操作</span></div></div><div class="server-grid">${app.state.servers
    .filter((s) =>
      (s.config.name + " " + s.config.region + " " + s.config.address)
        .toLowerCase()
        .includes(app.query.toLowerCase()),
    )
    .map(serverCard)
    .join(
      "",
    )}</div>${!app.state.servers.length ? `<div class="empty">${icon("server")}<h2>尚未添加服务器</h2><p>等待探针接入</p>${admin() ? button("add-server", "plus", "添加服务器") : ""}</div>` : ""}${alerts.length ? `<section class="section"><div class="section-title"><h2>当前告警</h2><span class="small muted">${alerts.length} 项</span></div>${alerts.slice(0, 8).map(alertRow).join("")}</section>` : ""}`;
}
function serverCard(s) {
  const snap = s.snapshot;
  const valid = fresh(s);
  const mem = snap.memoryTotal ? (snap.memoryUsed / snap.memoryTotal) * 100 : 0;
  const disks = snap.disks || [];
  const disk = disks[0];
  const count = snap.containers?.length || 0;
  const running =
    snap.containers?.filter((c) => c.state === "running").length || 0;
  const errors = app.state.alerts.filter((a) => a.serverID === s.id).length;
  return `<article class="server-card"><div class="card-top"><div><button class="server-name" data-action="detail" data-id="${esc(s.id)}">${esc(s.config.name)}</button><p class="sub">${esc(s.config.region || snap.hostname || "—")} · ${esc(s.config.address || snap.os || "等待接入")}</p></div>${badge(s.config.enabled ? (s.online ? "在线" : "离线") : "已停用", s.config.enabled ? (s.online ? "good" : "bad") : "")}</div><div class="metrics">${metric("CPU", valid ? snap.cpu : null)}${metric("内存", valid ? mem : null, valid ? `${bytes(snap.memoryUsed)} / ${bytes(snap.memoryTotal)}` : "—")}${metric(disk ? "磁盘 " + disk.path : "磁盘", valid && disk ? (1 - disk.free / disk.total) * 100 : null, valid && disk ? `可用 ${bytes(disk.free)}` : "—")}${metric("容器", null, valid ? running + " / " + count + " 运行" : "状态未知")}</div>${networkStrip(s)}<div class="card-bottom"><span class="small muted">${esc(age(s.lastSeen))}${s.maintenanceUntil > app.state.now ? " · 维护中" : ""}</span><div class="controls">${errors ? badge(errors + " 项告警", "bad") : ""}${button("detail", "arrow-up-right", "查看详情", `data-id="${esc(s.id)}"`)}</div></div></article>`;
}
function alertRow(a) {
  const s = app.state.servers.find((x) => x.id === a.serverID);
  return `<div class="alert-row">${icon(a.level === "critical" ? "circle-alert" : "triangle-alert")}<div><strong>${esc(a.title)}</strong><p class="small muted">${esc(s?.config.name || "监控服务")} · ${esc(time(a.since))}</p><p class="small">${esc(a.detail)}</p></div>${admin() && !a.acknowledged ? button("ack", "check", "确认", `data-key="${esc(a.key)}"`) : badge(a.acknowledged ? "已确认" : "待处理", "warn")}</div>`;
}
function containerResourceSummary(s) {
  const running = (s.snapshot.containers || []).filter((c) => c.state === "running");
  const cpu = running.filter((c) => c.cpuReady);
  const memory = running.filter((c) => c.memoryReady);
  if (!cpu.length && !memory.length)
    return '<p class="small muted">等待两个连续的容器资源采样</p>';
  const cpuTotal = cpu.reduce((total, c) => total + c.cpuPercent, 0);
  const memoryTotal = memory.reduce((total, c) => total + c.memoryHostPercent, 0);
  return `<div class="health-strip container-resource-summary"><span>容器合计（整机口径）CPU ${cpu.length ? cpuTotal.toFixed(1) + "%" : "—"} · 内存 ${memory.length ? memoryTotal.toFixed(1) + "%" : "—"}</span><span class="small muted">有效采样 ${Math.max(cpu.length, memory.length)} / ${running.length} 个运行容器；主机进程、内核和共享内存不计入</span></div>`;
}
function detailView() {
  const s = app.state.servers.find((x) => x.id === app.detail);
  if (!s) {
    app.detail = "";
    return monitorView();
  }
  const snap = s.snapshot,
    valid = fresh(s);
  return `<div class="heading"><div><div class="controls">${button("back", "arrow-left", "返回")}<h1>${esc(s.config.name)}</h1>${badge(s.online ? "在线" : "离线", s.online ? "good" : "bad")}</div><p class="sub">${esc(snap.hostname || "等待探针")} · ${esc(s.config.address)} · 最后采样 ${esc(time(s.sampleAt))}</p></div><div class="controls">${button("refresh", "刷新状态")}${admin() ? button("edit-server", "settings-2", "设置", `data-id="${esc(s.id)}"`) : ""}${admin() ? button("maintenance", "wrench", s.maintenanceUntil > app.state.now ? "结束维护" : "维护模式", `data-id="${esc(s.id)}"`) : ""}</div></div>${!valid ? '<div class="notice error">当前采样过期或探针离线，容器状态待核实。</div>' : ""}<div class="detail-grid"><div><span class="small muted">CPU · ${snap.cores || 0} 核</span><strong>${valid ? (snap.cpu || 0).toFixed(1) + "%" : "—"}</strong></div><div><span class="small muted">内存</span><strong>${valid ? bytes(snap.memoryUsed) + " / " + bytes(snap.memoryTotal) : "—"}</strong></div><div><span class="small muted">运行时间</span><strong>${valid ? Math.floor((snap.uptime || 0) / 86400) + " 天 " + Math.floor(((snap.uptime || 0) % 86400) / 3600) + " 小时" : "—"}</strong></div></div><div class="trend" id="trend" aria-label="最近 CPU 采样趋势"></div><section class="section"><div class="section-title"><h2>容器</h2><span class="small muted">${snap.containers?.length || 0} 个</span></div>${containerResourceSummary(s)}${containerTable(s)}</section>${(snap.errors || []).length ? `<div class="notice error">${esc(snap.errors.join("；"))}</div>` : ""}<section class="section"><h2>磁盘与系统服务</h2><div class="check-list">${(snap.disks || []).map((d) => `<div class="check-item"><div><strong>${esc(d.path)}</strong><p class="small muted">总量 ${bytes(d.total)} · inode 可用 ${(d.inodeFree || 0).toFixed(1)}%</p></div><span>可用 ${bytes(d.free)}</span></div>`).join("")}${(snap.units || []).map((u) => `<div class="check-item"><div><strong>${esc(u.name)}</strong><p class="small muted">${esc(u.subState)}</p></div>${badge(u.state)}</div>`).join("")}</div></section>${(snap.checks || []).length ? `<section class="section"><h2>HTTP 检查</h2>${snap.checks.map((x) => `<div class="check-item"><div><strong>${esc(x.name)}</strong><p class="small muted">${esc(time(x.at))} · ${x.millis} ms · ${x.status || esc(x.error)}</p></div>${badge(x.ok ? "通过" : "失败", x.ok ? "good" : "bad")}</div>`).join("")}</section>` : ""}${s.config.backup.enabled ? `<section class="section"><h2>备份检查</h2><div class="health-strip"><span>最近完成：${esc(time(snap.backup?.successAt))}</span><span>备份服务：${snap.backup?.running ? "运行中" : "未运行"}</span><span>服务恢复：${snap.backup?.servicesHealthy ? "健康" : "待核实"}</span></div><p class="small muted">${esc(snap.backup?.error)}</p></section>` : ""}<section class="section"><h2>当前告警</h2>${
    app.state.alerts
      .filter((a) => a.serverID === s.id)
      .map(alertRow)
      .join("") || '<p class="sub">暂无告警</p>'
  }</section>${networkView(s)}${agentUpdateSection(s)}`;
}
function containerTable(s, settings = false) {
  const list = s.snapshot.containers || [];
  const merged = new Map(list.map((c) => [c.name, c]));
  if (settings)
    for (const name of Object.keys(app.server.containers))
      if (!merged.has(name))
        merged.set(name, { name, state: "missing", health: "none" });
  if (!merged.size)
    return (
      '<div class="empty">' + icon("container") + "<p>尚无容器采样</p></div>"
    );
  return `<div class="table-scroll"><table class="${settings ? "" : "container-resource-table"}"><thead><tr><th>容器 / 镜像</th><th>状态</th>${settings ? "" : "<th>CPU（整机）</th><th>内存（用量 / 限额）</th>"}<th>${settings ? "监控" : "重启次数"}</th><th>${settings ? "允许重启" : "启动时间"}</th><th>${settings ? "数据库" : "操作"}</th></tr></thead><tbody>${[
    ...merged.values(),
  ]
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((c) => {
      const r = (settings ? app.server : s.config).containers[c.name] || {
        monitor: (settings ? app.server : s.config).monitorNew,
        restart: false,
        database: c.database,
      };
      const resources = settings ? "" : `<td class="mono">${c.cpuReady ? c.cpuPercent.toFixed(1) + "%" : "—"}</td><td>${c.memoryReady ? `${bytes(c.memoryUsage)} / ${bytes(c.memoryLimit)}<div class="sub">限额 ${c.memoryPercent.toFixed(1)}% · 整机 ${c.memoryHostPercent.toFixed(1)}%</div>` : "—"}</td>`;
      return `<tr><td class="name-cell"><strong>${esc(c.name)}</strong><div class="sub mono">${esc(c.image || "尚无采样")}</div></td><td>${badge(c.state)}<div class="sub">${badge(c.health || "none")}</div></td>${resources}${settings ? ["monitor", "restart", "database"].map((k) => `<td><label class="check"><input type="checkbox" data-container="${esc(c.name)}" data-rule="${k}" aria-label="${esc(c.name)} ${k}" ${r[k] || (k === "database" && c.database) ? "checked" : ""} ${k === "restart" && !r.monitor ? "disabled" : ""}><span class="sr-only">${k}</span></label></td>`).join("") : `<td>${c.restarts || 0}</td><td class="small muted">${c.startedAt ? esc(time(Date.parse(c.startedAt) / 1000)) : "—"}</td><td><div class="controls">${admin() ? `${button("logs", "读取日志", "", `data-server="${esc(s.id)}" data-container="${esc(c.id)}" ${!fresh(s) || !app.state.allowLogs ? "disabled" : ""}`)}${button("restart", "重启容器", "", `data-server="${esc(s.id)}" data-container="${esc(c.id)}" ${!fresh(s) || !app.state.manualRestart || !r.monitor || !r.restart ? "disabled" : ""}`)}` : "—"}</div></td>`}</tr>`;
    })
    .join("")}</tbody></table></div>`;
}
function eventsView() {
  let events = app.state.events.filter(
    (x) =>
      (!app.serverFilter || x.serverID === app.serverFilter) &&
      (x.title + " " + x.detail + " " + x.actor)
        .toLowerCase()
        .includes(app.query.toLowerCase()),
  );
  return `<div class="heading"><div><h1>事件与操作</h1><p class="sub">最近 ${app.state.events.length} 条事件</p></div><div class="controls"><select id="event-server" aria-label="筛选服务器"><option value="">全部服务器</option>${app.state.servers.map((s) => `<option value="${esc(s.id)}" ${s.id === app.serverFilter ? "selected" : ""}>${esc(s.config.name)}</option>`).join("")}</select><input id="event-query" type="search" placeholder="搜索事件" aria-label="搜索事件" value="${esc(app.query)}">${button("refresh", "刷新事件")}</div></div><h2>容器操作</h2><div class="table-scroll"><table><thead><tr><th>容器</th><th>操作 / 状态</th><th>提交人</th><th>时间</th><th>回执</th></tr></thead><tbody>${
    app.state.jobs
      .filter((j) => !app.serverFilter || j.serverID === app.serverFilter)
      .map(
        (j) =>
          `<tr><td>${esc(j.containerName)}</td><td>${j.action === "restart" ? "重启" : "日志"} ${badge(j.state)}</td><td>${esc(j.actor)}</td><td class="small">${esc(time(j.created))}</td><td><button data-action="job-result" data-id="${esc(j.id)}">${icon("file-check-2")}查看回执</button></td></tr>`,
      )
      .join("") || '<tr><td colspan="5" class="muted">暂无操作</td></tr>'
  }</tbody></table></div><section class="section"><h2>事件记录</h2>${events.map((x) => `<div class="event-row">${icon(x.kind === "action" ? "rotate-cw" : x.kind === "alert" ? "triangle-alert" : x.kind === "recovery" ? "circle-check" : "circle-dot")}<div><strong>${esc(x.title)}</strong><p class="small muted">${esc(app.state.servers.find((s) => s.id === x.serverID)?.config.name || "监控服务")}${x.actor ? " · " + esc(x.actor) : ""}</p><p class="small">${esc(x.detail)}</p></div><span class="time">${esc(time(x.at))}</span></div>`).join("") || '<div class="empty">暂无事件</div>'}</section>`;
}

// A single schema supplies visible controls for every persisted operator setting.
const schemas = {
  servers: [
    ["name", "服务器名称", "text"],
    ["region", "区域 / 分组", "text"],
    ["address", "地址备注", "text"],
    ["enabled", "启用监控", "check"],
    ["heartbeat", "采样间隔（秒）", "number", 10, 300],
    ["heartbeatTimeout", "失联阈值（秒）", "number", 21, 1800],
    ["commandPoll", "命令轮询（秒）", "number", 1, 60],
    ["verifyTLS", "验证监控端 HTTPS 证书", "check"],
    ["proxy", "探针出站代理", "url"],
  ],
  targets: [
    ["dockerSocket", "Docker Socket", "text"],
    ["autoDiscover", "发现未配置的容器", "check"],
    ["monitorNew", "新发现容器自动纳入监控", "check"],
    ["disks", "磁盘路径（每行一个）", "textarea"],
    ["units", "systemd 服务（每行一个）", "textarea"],
  ],
  network: [
    ["network.automatic", "自动识别流量异常", "check"],
  ],
  alerts: [
    ["alerts.interval", "巡检间隔（秒）", "number", 10, 300],
    ["alerts.failures", "连续失败采样数", "number", 1, 10],
    ["alerts.diskWarn", "磁盘预警剩余量（GiB）", "number", 1, 100000],
    ["alerts.diskCritical", "磁盘严重剩余量（GiB）", "number", 1, 100000],
    ["alerts.cpu", "CPU 阈值（%）", "number", 1, 100],
    ["alerts.memory", "内存阈值（%）", "number", 1, 100],
    ["alerts.sustained", "CPU / 内存持续时间（秒）", "number", 10, 1800],
    ["alerts.inode", "inode 剩余阈值（%）", "number", 1, 99],
    ["alerts.restartCount", "频繁重启次数", "number", 1, 30],
    ["alerts.restartWindow", "重启统计窗口（秒）", "number", 30, 3600],
    ["alerts.repeatMinutes", "未确认告警重复提醒（分钟）", "number", 1, 1440],
    ["alerts.maintenanceMinutes", "默认维护窗口（分钟）", "number", 1, 1440],
  ],
  backup: [
    ["backup.enabled", "启用备份检查", "check"],
    ["backup.unit", "备份 systemd 服务", "text"],
    ["backup.reportPattern", "报告路径 / 通配符", "text"],
    ["backup.start", "计划开始时间", "time"],
    ["backup.deadline", "完成截止时间", "time"],
    ["backup.maxMinutes", "最长执行时间（分钟）", "number", 1, 720],
    ["backup.maxAgeHours", "成功报告最长间隔（小时）", "number", 1, 168],
    ["backup.requireHealthy", "要求备份后服务恢复健康", "check"],
  ],
  notifications: [
    [
      "notifications.channel",
      "通知渠道",
      "select",
      [
        ["feishu", "飞书"],
        ["wecom", "企业微信"],
        ["bark", "Bark"],
        ["smtp", "邮件 SMTP"],
        ["webhook", "通用 Webhook"],
      ],
    ],
    ["notifications.name", "渠道名称", "text"],
    ["notifications.notices", "启用通知", "check"],
    ["notifications.recovery", "发送恢复通知", "check"],
    ["notifications.actions", "发送操作回执通知", "check"],
    ["notifications.groupWindow", "消息合并窗口（秒）", "number", 0, 300],
    ["notifications.timeout", "发送超时（秒）", "number", 1, 60],
    ["notifications.retries", "失败重试次数", "number", 0, 10],
  ],
  access: [
    ["access.sessionHours", "会话最长有效期（小时）", "number", 1, 720],
    ["access.idleMinutes", "空闲失效（分钟）", "number", 1, 1440],
    ["access.loginFailures", "连续登录失败上限", "number", 3, 20],
    ["access.lockMinutes", "登录锁定（分钟）", "number", 1, 1440],
    ["access.allowIPs", "允许访问 IP / CIDR（留空为全部）", "textarea"],
    ["access.manualRestart", "允许手动重启容器", "check"],
    ["access.allowLogs", "允许读取容器日志", "check"],
    ["access.databaseConfirm", "数据库重启需输入容器名", "check"],
    ["access.requireReason", "重启必须填写备注", "check"],
    ["access.commandTimeout", "重启请求超时（秒）", "number", 5, 300],
    ["access.healthWait", "重启后健康等待（秒）", "number", 5, 300],
    ["access.restartCooldown", "重启冷却（秒）", "number", 0, 3600],
  ],
  service: [
    ["service.siteName", "站点名称", "text"],
    ["service.publicURL", "对外访问地址", "url"],
    ["service.listenAddress", "监听 IP", "text"],
    ["service.listenPort", "监听端口", "number", 1, 65535],
    ["service.timezone", "时区", "text"],
    [
      "service.tlsMode",
      "HTTPS 模式",
      "select",
      [
        ["proxy", "外部反向代理"],
        ["builtin", "内置 HTTPS"],
      ],
    ],
    ["service.trustedProxies", "受信任代理 IP / CIDR", "textarea"],
    ["service.requestTimeout", "HTTP 请求超时（秒）", "number", 5, 300],
    ["service.maxOps", "同时执行操作上限", "number", 1, 10],
    ["service.publicStatus", "开放匿名汇总状态接口", "check"],
  ],
  storage: [
    ["storage.dataDirectory", "数据目录（绝对路径）", "text"],
    ["storage.metricsDays", "指标保留（天）", "number", 1, 365],
    ["storage.eventsDays", "事件保留（天）", "number", 1, 365],
    ["storage.auditDays", "操作审计保留（天）", "number", 1, 730],
    ["storage.logDays", "运行日志保留（天）", "number", 1, 90],
    ["storage.logCap", "运行日志总量（MiB）", "number", 10, 2048],
    ["storage.logFileCap", "单份运行日志（MiB）", "number", 1, 100],
    ["storage.databaseCap", "数据库容量上限（MiB）", "number", 32, 10240],
    ["storage.minFree", "历史采样与备份磁盘预留（GiB）", "number", 1, 100],
    ["storage.dataBackup", "自动备份监控数据", "check"],
    ["storage.dataBackupTime", "每日数据备份时间", "time"],
    ["storage.dataBackupKeep", "保留备份份数", "number", 1, 30],
  ],
};
function fields(schema, object = app.draft, prefix = "") {
  return schema
    .map(([path, label, type, min, max]) => {
      path = prefix + path;
      const v = get(object, path);
      const id = "f-" + path.replaceAll(".", "-");
      const attr = `id="${id}" data-bind="${path}"`;
      if (type === "check")
        return `<label class="check"><input ${attr} type="checkbox" ${v ? "checked" : ""}><span>${label}</span></label>`;
      let input;
      if (type === "select") {
        input = `<select ${attr}>${min.map(([value, text]) => `<option value="${value}" ${v === value ? "selected" : ""}>${text}</option>`).join("")}</select>`;
      } else if (type === "textarea") {
        input = `<textarea ${attr} rows="3">${esc(v)}</textarea>`;
      } else {
        input = `<input ${attr} type="${type}" value="${esc(v)}" ${type === "number" ? `required min="${min}" max="${max}" step="1"` : ""}>`;
      }
      return `<label class="${type === "textarea" ? "field-wide" : ""}"><span>${label}</span>${input}</label>`;
    })
    .join("");
}
function secretField(key, label, multiline = false) {
  const present = app.settings.secrets[key];
  return `<label class="${multiline ? "field-wide" : ""}"><span>${label} · ${present ? "已保存" : "未设置"}</span>${multiline ? `<textarea data-secret="${key}" rows="3" autocomplete="off" spellcheck="false" placeholder="${present ? "留空保留原值" : "尚未设置"}">${esc(app.secrets[key] || "")}</textarea>` : `<input data-secret="${key}" type="password" value="${esc(app.secrets[key] || "")}" autocomplete="new-password" placeholder="${present ? "留空保留原值" : "尚未设置"}">`}${present ? `<span class="secret-row"><span class="check"><input type="checkbox" data-clear-secret="${key}" ${app.clearSecrets.has(key) ? "checked" : ""}><span>清除已保存凭据</span></span></span>` : ""}</label>`;
}
function settingsView() {
  if (!app.settings) return '<div class="loading">正在读取设置…</div>';
  const category = categories.find((x) => x[0] === app.category);
  const serverCategory = ["servers", "targets", "network", "backup"].includes(
    app.category,
  );
  return `<div class="heading"><div><h1>系统设置</h1><p class="sub">${esc(app.settings.applyStatus || "全部设置保存在监控服务数据库")}</p></div></div><div class="settings-layout"><aside class="settings-side" aria-label="设置分类">${categories.map(([id, i, name]) => `<button data-action="category" data-category="${id}" class="${app.category === id ? "selected" : ""}">${icon(i)}${name}</button>`).join("")}</aside><div class="settings-content"><select id="category-select" class="category-mobile" aria-label="设置分类">${categories.map(([id, , name]) => `<option value="${id}" ${app.category === id ? "selected" : ""}>${name}</option>`).join("")}</select><div class="section-title"><h2>${category[2]}</h2>${serverCategory ? button("add-server", "plus", "添加服务器") : ""}</div>${serverCategory ? `<div class="inline-form"><label><span>服务器</span><select id="settings-server">${!app.serverID ? '<option value="">新服务器</option>' : ""}${app.state.servers.map((s) => `<option value="${esc(s.id)}" ${s.id === app.serverID ? "selected" : ""}>${esc(s.config.name)}</option>`).join("")}</select></label>${app.serverID ? button("enrollment", "link", "接入探针") : ""}</div>` : ""}<form id="settings-form" data-scope="${serverCategory ? "server" : "global"}"><div class="form-grid">${fields(schemas[app.category], serverCategory ? app.server : app.draft)}</div>${settingsExtra()}<div class="form-actions"><button class="primary" type="submit">${icon("save")}保存设置</button>${["service", "storage"].includes(app.category) ? button("apply", "check-check", "应用服务设置") : ""}${app.category === "notifications" ? button("test-notification", "send", "发送测试通知") : ""}<span class="small muted" id="dirty-status">${app.globalDirty || app.serverDirty ? "有未保存修改" : ""}</span></div></form>${app.category === "access" ? usersView() : ""}</div></div>`;
}
function settingsExtra() {
  const cat = app.category;
  if (cat === "network") {
    const s = app.state.servers.find((x) => x.id === app.serverID);
    return `<div class="form-section"><h3>监测状态</h3>${s ? automaticNetworkStatus(s) : badge("等待探针接入")}<details class="network-options"><summary>网卡选择（可选）</summary><div class="form-grid">${fields([["network.interfaces", "网卡（每行一个，留空自动选择）", "textarea"]], app.server)}</div></details></div>`;
  }
  if (cat === "servers") {
    const s = app.state.servers.find((s) => s.id === app.serverID);
    return `<div class="form-section"><h3>探针状态</h3><p class="small muted">${s ? esc(age(s.lastSeen)) : "尚未创建服务器"}</p>${agentUpdateContent(s)}${app.serverID ? `<div class="form-actions">${button("revoke", "key-round", "撤销探针凭据")}${button("delete-server", "trash-2", "移除服务器")}</div>` : ""}</div>`;
  }
  if (cat === "targets") {
    const s = app.state.servers.find((x) => x.id === app.serverID) || {
      snapshot: { containers: [] },
    };
    return `<div class="form-section"><h3>容器权限</h3>${containerTable(s, true)}<div class="inline-form"><label><span>未发现的容器名称</span><input id="container-name" maxlength="128" placeholder="例如 postgres"></label>${button("add-container", "plus", "加入监控")}</div></div><div class="form-section"><div class="section-title"><h3>HTTP 检查</h3>${button("add-check", "plus", "添加检查")}</div>${
      app.server.checks
        .map(
          (c, i) =>
            `<div class="check-config"><div class="form-grid">${fields(
              [
                ["name", "检查名称", "text"],
                ["url", "检查地址", "url"],
                ["status", "预期状态码", "number", 100, 599],
                ["timeout", "超时（秒）", "number", 1, 60],
                [
                  "location",
                  "执行位置",
                  "select",
                  [
                    ["agent", "被监控服务器"],
                    ["hub", "监控服务"],
                  ],
                ],
                ["container", "关联容器名称", "text"],
                ["verifyTLS", "验证 HTTPS 证书", "check"],
              ],
              app.server,
              `checks.${i}.`,
            )}</div><div class="controls">${button("remove-check", "trash-2", "删除检查", `data-index="${i}"`)}</div></div>`,
        )
        .join("") || '<p class="sub">暂无 HTTP 检查</p>'
    }</div>`;
  }
  if (cat === "notifications") {
    const channel = app.draft.notifications.channel;
    let content = "";
    if (channel === "feishu")
      content =
        secretField("feishuEndpoint", "飞书 Webhook 地址") +
        secretField("feishuSecret", "飞书签名密钥");
    if (channel === "wecom")
      content = secretField("wecomEndpoint", "企业微信 Webhook 地址");
    if (channel === "bark")
      content =
        fields([["notifications.barkServer", "Bark 服务地址", "url"]]) +
        secretField("barkKey", "设备密钥");
    if (channel === "webhook")
      content =
        secretField("webhookEndpoint", "Webhook 地址") +
        secretField("webhookSecret", "HMAC 签名密钥");
    if (channel === "smtp")
      content =
        fields([
          ["notifications.smtpHost", "SMTP 主机", "text"],
          ["notifications.smtpPort", "SMTP 端口", "number", 1, 65535],
          [
            "notifications.smtpTLS",
            "加密方式",
            "select",
            [
              ["implicit", "TLS"],
              ["starttls", "STARTTLS"],
            ],
          ],
          ["notifications.smtpUser", "SMTP 用户名", "text"],
          ["notifications.smtpFrom", "发件地址", "email"],
          ["notifications.smtpTo", "收件地址（逗号分隔）", "text"],
        ]) + secretField("smtpPassword", "SMTP 密码");
    return `<div class="form-section"><h3>${esc(app.draft.notifications.name || "渠道凭据")}</h3><div class="form-grid">${content}</div></div>`;
  }
  if (cat === "service")
    return `<div class="form-section"><h3>当前服务</h3><p class="mono small">${esc(app.settings.activeService.listenAddress)}:${app.settings.activeService.listenPort} · ${app.settings.activeService.tlsMode === "builtin" ? "HTTPS" : "HTTP / 反向代理"}</p>${app.draft.service.tlsMode === "builtin" ? `<div class="form-grid">${secretField("tlsCertificate", "PEM 证书链", true)}${secretField("tlsPrivateKey", "PEM 私钥", true)}</div>` : ""}</div>`;
  if (cat === "storage")
    return `<div class="form-section"><h3>当前数据目录</h3><p class="mono small">${esc(app.settings.activeData)}</p><div class="form-actions">${button("backup-data", "database-backup", "立即备份监控数据")}</div></div>`;
  if (cat === "backup") {
    const snap = app.state.servers.find((x) => x.id === app.serverID)?.snapshot;
    return `<div class="form-section"><h3>最近报告</h3><div class="health-strip"><span>完成：${esc(time(snap?.backup?.successAt))}</span><span>服务恢复：${snap?.backup?.servicesHealthy ? "健康" : "待核实"}</span></div><p class="small muted">${esc(snap?.backup?.error)}</p></div>`;
  }
  return "";
}
function usersView() {
  return `<section class="section"><div class="section-title"><h2>账号</h2>${button("new-user", "user-plus", "添加账号")}</div><div class="table-scroll"><table><thead><tr><th>用户名</th><th>角色</th><th>状态</th><th>操作</th></tr></thead><tbody>${app.settings.users.map((u) => `<tr><td>${esc(u.username)}</td><td>${u.role === "admin" ? "管理员" : "只读"}</td><td>${badge(u.enabled ? "启用" : "停用", u.enabled ? "good" : "")}</td><td>${button("edit-user", "pencil", "编辑", `data-id="${esc(u.id)}"`)}</td></tr>`).join("")}</tbody></table></div></section>`;
}
function openDialog(title, content) {
  const d = $("#dialog");
  d.innerHTML = `<div class="dialog-head"><h2>${esc(title)}</h2>${button("close", "关闭")}</div><div class="dialog-body">${content}<div id="dialog-status" role="status" aria-live="polite"></div></div>`;
  if (!d.open) d.showModal();
  icons();
}
async function saveSettings(form) {
  if (!form.reportValidity()) return;
  const server = form.dataset.scope === "server";
  if (server) {
    const result = await api("server/save", {
      id: app.serverID,
      revision: app.serverRevision,
      config: app.server,
    });
    await loadState();
    selectServer(result.id);
    message("服务器设置已保存，探针将在下次连接时同步");
  } else {
    await api("config/save", {
      config: app.draft,
      revision: app.settings.revision,
      secrets: app.secrets,
      clearSecrets: [...app.clearSecrets],
    });
    await loadSettings();
    app.siteName = app.draft.service.siteName;
    message(
      "系统设置已保存" +
        (["service", "storage"].includes(app.category)
          ? "，服务参数可点击应用"
          : ""),
    );
  }
  render();
}
async function showEnrollment() {
  if (app.serverDirty) throw Error("请先保存服务器设置");
  if (!app.serverID) throw Error("请先创建服务器");
  const result = await api("server/enrollment", { id: app.serverID });
  const hub = app.draft.service.publicURL || location.origin;
  const quote = (s) => "'" + String(s).replaceAll("'", "'\\''") + "'";
  const command = `sudo ./system-monitor agent enroll --hub ${quote(hub)} --id ${quote(app.serverID)} --code ${quote(result.code)}${app.server.verifyTLS ? "" : " --insecure"}${app.server.proxy ? " --proxy " + quote(app.server.proxy) : ""}`;
  openDialog(
    "接入探针",
    `<p class="small muted">一次性接入码 · ${esc(time(result.expires))} 过期</p><pre id="enrollment-command">${esc(command)}</pre><div class="dialog-footer">${button("copy-enrollment", "copy", "复制命令")}${button("close", "check", "完成")}</div>`,
  );
}
function showRestart(serverID, containerID) {
  const s = app.state.servers.find((x) => x.id === serverID);
  const c = s?.snapshot.containers.find((x) => x.id === containerID);
  if (!c) throw Error("容器已变化，请刷新");
  const db =
    app.state.databaseConfirm &&
    (c.database || s.config.containers[c.name]?.database);
  openDialog(
    "重启 " + c.name,
    `<p class="small muted">${esc(s.config.name)} · ${esc(c.id.slice(0, 12))}</p><form id="restart-form" data-server="${esc(s.id)}" data-container="${esc(c.id)}">${db ? `<label><span>输入数据库容器名称确认：${esc(c.name)}</span><input name="confirmation" required autocomplete="off"></label>` : ""}<label><span>重启备注</span><input name="remark" maxlength="500" ${app.draft?.access.requireReason ? "required" : ""} autocomplete="off"></label><div class="dialog-footer">${button("close", "x", "取消")}<button class="primary" type="submit">${icon("rotate-cw")}确认重启</button></div></form>`,
  );
}
let jobPoll = 0;
function showAgentUpdate(serverID) {
  if (app.serverDirty) throw Error("请先保存服务器设置");
  const s = app.state.servers.find((s) => s.id === serverID);
  if (!s?.agentUpdate?.available) throw Error(s?.agentUpdate?.reason || "当前无法更新探针");
  const target = s.agentUpdate.target;
  openDialog("更新探针", `<p>${esc(s.config.name)}</p><div class="agent-version-grid"><div><span class="small muted">当前版本</span><p>${esc(agentVersionLabel(s.snapshot.agentVersion, s.snapshot.agent?.revision))}</p></div><div><span class="small muted">目标版本</span><p>${esc(agentVersionLabel(target.version, target.revision))}</p></div></div><p class="small muted">更新期间探针会短暂重连，原有身份和监控设置保留。</p><div class="dialog-footer">${button("close", "x", "取消")}${button("confirm-agent-update", "download", "确认更新", `class="primary" data-id="${esc(s.id)}" data-sha="${esc(target.sha256)}"`)}</div>`);
}
async function showAgentUpdateResult(id) {
  clearTimeout(jobPoll);
  const u = await api("agent-update?id=" + encodeURIComponent(id));
  openDialog("探针更新记录", `<div class="badges">${agentUpdateBadge(u.state)}</div><p class="status-line">${esc(u.result || "等待探针领取更新任务")}</p><p class="small muted">${esc(agentVersionLabel(u.target.version, u.target.revision))}</p><p class="small muted">${esc(u.actor)} · ${esc(time(u.created))}</p><div class="dialog-footer">${button("close", "check", "关闭")}</div>`);
  if (["queued", "downloading", "restarting"].includes(u.state))
    jobPoll = setTimeout(() => {
      if ($("#dialog").open) showAgentUpdateResult(id).catch((e) => { $("#dialog-status").textContent = e.message; });
    }, 1500);
}
async function showJob(id) {
  const j = await api("job?id=" + encodeURIComponent(id));
  openDialog(
    j.containerName + " · " + (j.action === "restart" ? "重启回执" : "日志"),
    `<div class="badges">${badge(j.state)}</div><p class="status-line">${esc(j.result || "等待探针确认")}</p><p class="small muted">${esc(j.actor)} · ${esc(time(j.created))}${j.remark ? " · " + esc(j.remark) : ""}</p>${j.logs ? `<pre>${esc(j.logs)}</pre>` : ""}<div class="dialog-footer">${button("close", "check", "关闭")}</div>`,
  );
  clearTimeout(jobPoll);
  if (["queued", "leased"].includes(j.state))
    jobPoll = setTimeout(() => {
      if ($("#dialog").open)
        showJob(id).catch((e) => {
          $("#dialog-status").textContent = e.message;
        });
    }, 1500);
}
function userDialog(id) {
  const u = app.settings.users.find((x) => x.id === id) || {
    id: "",
    username: "",
    role: "viewer",
    enabled: true,
  };
  openDialog(
    u.id ? "编辑账号" : "添加账号",
    `<form id="user-form" data-id="${esc(u.id)}"><label><span>用户名</span><input name="username" value="${esc(u.username)}" required minlength="3" maxlength="64" autocomplete="off"></label><label><span>角色</span><select name="role"><option value="viewer" ${u.role === "viewer" ? "selected" : ""}>只读</option><option value="admin" ${u.role === "admin" ? "selected" : ""}>管理员</option></select></label><label class="check"><input name="enabled" type="checkbox" ${u.enabled ? "checked" : ""}><span>启用账号</span></label><label><span>密码${u.id ? "（留空保留原值）" : " · 至少 12 字节"}</span><input name="password" type="password" autocomplete="new-password" ${u.id ? "" : "required"} maxlength="256"></label><div class="dialog-footer"><button class="primary" type="submit">${icon("save")}保存账号</button></div></form>`,
  );
}
async function refresh() {
  await loadState();
  if (app.view === "settings" && !app.globalDirty && !app.serverDirty) {
    const id = app.serverID;
    await loadSettings();
    if (id) selectServer(id);
  }
  render();
}
async function loadTrend() {
  try {
    const serverID = app.detail;
    const data = await api("history?id=" + encodeURIComponent(serverID));
    if (app.detail !== serverID || app.view !== "monitor") return;
    data.reverse();
    const el = $("#trend");
    if (el)
      el.innerHTML = data
        .map(
          (x) =>
            `<i style="--height:${Math.max(2, Math.min(100, x.cpu))}%" title="${esc(time(x.at))} · CPU ${Number(x.cpu).toFixed(1)}%"></i>`,
        )
        .join("");
    const network = $("#network-trend");
    if (network) {
      const valid = data.filter((x) => x.receiveBytesPerSecond != null);
      const peak = Math.max(0, ...valid.flatMap((x) => [x.receiveBytesPerSecond, x.transmitBytesPerSecond]));
      const scale = Math.max(1, peak);
      $("#network-scale").textContent = valid.length ? "峰值 " + mbps(peak) : "尚无有效流量采样";
      network.innerHTML = data.map((x) => {
        if (x.receiveBytesPerSecond == null)
          return `<span class="network-sample missing" title="${esc(time(x.at))} · 速率未知"></span>`;
        const title = `${time(x.at)} · 入站 ${mbps(x.receiveBytesPerSecond)} · 出站 ${mbps(x.transmitBytesPerSecond)} · 入站 ${x.receivePacketsPerSecond.toFixed(0)} 包/秒`;
        return `<span class="network-sample" title="${esc(title)}"><i class="receive" style="--height:${Math.max(1, x.receiveBytesPerSecond / scale * 100)}%"></i><i class="transmit" style="--height:${Math.max(1, x.transmitBytesPerSecond / scale * 100)}%"></i></span>`;
      }).join("");
    }
  } catch {}
}
async function action(el) {
  const a = el.dataset.action;
  switch (a) {
    case "nav":
      app.view = el.dataset.view;
      app.query = "";
      render();
      break;
    case "category":
      app.category = el.dataset.category;
      render();
      break;
    case "theme":
      app.theme =
        app.theme === "system"
          ? "dark"
          : app.theme === "dark"
            ? "light"
            : "system";
      localStorage.setItem("monitor-theme", app.theme);
      theme();
      break;
    case "logout":
      await api("logout", {});
      app.user = null;
      app.csrf = "";
      app.settings = null;
      app.draft = null;
      app.secrets = {};
      app.server = null;
      app.notice = "";
      render();
      break;
    case "refresh":
      await refresh();
      message("状态已刷新");
      break;
    case "detail":
      app.detail = el.dataset.id;
      app.view = "monitor";
      render();
      break;
    case "back":
      app.detail = "";
      render();
      break;
    case "add-server":
      if (app.serverDirty && !confirm("放弃当前服务器未保存的修改？")) return;
      app.view = "settings";
      app.category = "servers";
      selectServer("");
      render();
      break;
    case "edit-server":
      if (app.serverDirty && !confirm("放弃当前服务器未保存的修改？")) return;
      selectServer(el.dataset.id);
      app.view = "settings";
      app.category = "targets";
      render();
      break;
    case "enrollment":
      await showEnrollment();
      break;
    case "agent-update":
      showAgentUpdate(el.dataset.id);
      break;
    case "confirm-agent-update": {
      const u = await api("server/agent-update", { id: el.dataset.id, sha256: el.dataset.sha });
      await showAgentUpdateResult(u.id);
      await loadState();
      render();
      break;
    }
    case "agent-update-result":
      await showAgentUpdateResult(el.dataset.update);
      break;
    case "copy-enrollment":
      await navigator.clipboard.writeText($("#enrollment-command").textContent);
      $("#dialog-status").textContent = "命令已复制";
      break;
    case "close":
      clearTimeout(jobPoll);
      $("#dialog").close();
      $("#dialog").innerHTML = "";
      break;
    case "maintenance": {
      const s = app.state.servers.find((x) => x.id === el.dataset.id);
      await api("server/maintenance", {
        id: s.id,
        enabled: s.maintenanceUntil < app.state.now,
      });
      await refresh();
      message("维护窗口已更新");
      break;
    }
    case "ack":
      await api("alert/ack", { key: el.dataset.key });
      await refresh();
      message("已确认告警，恢复状态仍以采样为准");
      break;
    case "restart":
      showRestart(el.dataset.server, el.dataset.container);
      break;
    case "logs": {
      const j = await api("job/create", {
        serverID: el.dataset.server,
        containerID: el.dataset.container,
        action: "logs",
        remark: "",
        confirmation: "",
      });
      await showJob(j.id);
      break;
    }
    case "job-result":
      await showJob(el.dataset.id);
      break;
    case "add-container": {
      const name = $("#container-name").value.trim();
      if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/.test(name))
        throw Error("容器名称无效");
      app.server.containers[name] = {
        monitor: true,
        restart: false,
        database: false,
      };
      app.serverDirty = true;
      render();
      break;
    }
    case "add-check":
      if (app.server.checks.length >= 24)
        throw Error("每台服务器最多 24 个检查");
      app.server.checks.push({
        id: crypto.randomUUID?.() || Date.now().toString(36),
        name: "",
        url: "",
        status: 200,
        timeout: 5,
        location: "agent",
        verifyTLS: true,
        container: "",
      });
      app.serverDirty = true;
      render();
      break;
    case "remove-check":
      app.server.checks.splice(Number(el.dataset.index), 1);
      app.serverDirty = true;
      render();
      break;
    case "test-notification":
      if (app.globalDirty) throw Error("请先保存通知设置");
      await api("notification/test", {});
      message("测试通知已被通知平台接收");
      break;
    case "backup-data":
      await api("storage/backup", {});
      message("备份完成，已同时保存数据库与解密密钥");
      break;
    case "apply":
      if (app.globalDirty) throw Error("请先保存设置");
      await api("runtime/apply", {});
      message("正在应用服务设置，请使用保存后的地址连接");
      setTimeout(() => refresh().catch(() => {}), 2500);
      break;
    case "revoke":
      if (confirm("撤销后探针将停止连接，需要重新接入。继续？")) {
        await api("server/revoke", { id: app.serverID });
        await refresh();
        message("探针凭据已撤销");
      }
      break;
    case "delete-server": {
      const name = prompt("输入服务器名称以移除监控记录：");
      if (name == null) return;
      await api("server/delete", { id: app.serverID, name });
      selectServer("");
      await refresh();
      message("服务器已移除");
      break;
    }
    case "new-user":
      userDialog("");
      break;
    case "edit-user":
      userDialog(el.dataset.id);
      break;
  }
}
document.addEventListener("click", (e) => {
  const el = e.target.closest("[data-action]");
  if (!el || el.disabled) return;
  el.disabled = true;
  Promise.resolve(action(el))
    .catch((error) => {
      if ($("#dialog").open && $("#dialog-status"))
        $("#dialog-status").textContent = error.message;
      else message(error.message, true);
    })
    .finally(() => {
      if (el.isConnected) el.disabled = false;
    });
});
document.addEventListener("submit", (e) => {
  const form = e.target;
  if (
    !["auth-form", "settings-form", "restart-form", "user-form"].includes(
      form.id,
    )
  )
    return;
  e.preventDefault();
  const submit = form.querySelector("[type=submit]");
  submit.disabled = true;
  (async () => {
    if (form.id === "auth-form") {
      const values = Object.fromEntries(new FormData(form));
      if (app.setup) {
        if (values.trustedProxies.trim()) {
          values.proxy = {
            publicURL: values.publicURL.trim(),
            trustedProxies: values.trustedProxies.trim(),
          };
        }
        delete values.publicURL;
        delete values.trustedProxies;
      }
      const b = await api(app.setup ? "setup" : "login", values);
      app.user = b.user;
      app.csrf = b.csrf;
      app.setup = false;
      app.notice = "";
      await loadState();
      if (admin()) await loadSettings();
      render();
    }
    if (form.id === "settings-form") await saveSettings(form);
    if (form.id === "restart-form") {
      const values = Object.fromEntries(new FormData(form));
      const j = await api("job/create", {
        serverID: form.dataset.server,
        containerID: form.dataset.container,
        action: "restart",
        remark: values.remark || "",
        confirmation: values.confirmation || "",
      });
      await showJob(j.id);
      await loadState();
      render();
    }
    if (form.id === "user-form") {
      const values = Object.fromEntries(new FormData(form));
      await api("user/save", {
        id: form.dataset.id,
        username: values.username,
        password: values.password,
        role: values.role,
        enabled: values.enabled === "on",
      });
      $("#dialog").close();
      await loadSettings();
      render();
      message("账号已保存，相关旧会话已失效");
    }
  })()
    .catch((error) => {
      const target = $("#dialog").open ? $("#dialog-status") : null;
      if (target) target.textContent = error.message;
      else message(error.message, true);
    })
    .finally(() => {
      if (submit.isConnected) submit.disabled = false;
    });
});
document.addEventListener("input", (e) => {
  const el = e.target;
  if (el.dataset.bind) {
    const server = el.closest("form")?.dataset.scope === "server";
    const v =
      el.type === "checkbox"
        ? el.checked
        : el.type === "number"
          ? Number(el.value)
          : el.value;
    set(server ? app.server : app.draft, el.dataset.bind, v);
    if (server) app.serverDirty = true;
    else app.globalDirty = true;
    const status = $("#dirty-status");
    if (status) status.textContent = "有未保存修改";
    if (["notifications.channel", "service.tlsMode"].includes(el.dataset.bind))
      render();
  }
  if (el.dataset.secret) {
    app.secrets[el.dataset.secret] = el.value;
    app.globalDirty = true;
    if ($("#dirty-status")) $("#dirty-status").textContent = "有未保存修改";
  }
  if (el.dataset.clearSecret) {
    if (el.checked) app.clearSecrets.add(el.dataset.clearSecret);
    else app.clearSecrets.delete(el.dataset.clearSecret);
    app.globalDirty = true;
  }
  if (el.dataset.container) {
    const name = el.dataset.container;
    const existing = app.state.servers
      .find((s) => s.id === app.serverID)
      ?.snapshot.containers?.find((c) => c.name === name);
    const rule = app.server.containers[name] || {
      monitor: app.server.monitorNew,
      restart: false,
      database: existing?.database || false,
    };
    rule[el.dataset.rule] = el.checked;
    if (!rule.monitor) rule.restart = false;
    app.server.containers[name] = rule;
    app.serverDirty = true;
    render();
  }
  if (["server-query", "event-query"].includes(el.id)) {
    app.query = el.value;
    render();
  }
});
document.addEventListener("change", (e) => {
  const el = e.target;
  if (el.id === "category-select") {
    app.category = el.value;
    render();
  }
  if (el.id === "settings-server") {
    if (app.serverDirty && !confirm("放弃当前服务器未保存的修改？")) {
      el.value = app.serverID;
      return;
    }
    selectServer(el.value);
    render();
  }
  if (el.id === "event-server") {
    app.serverFilter = el.value;
    render();
  }
});
$("#dialog").addEventListener("close", () => clearTimeout(jobPoll));
window.addEventListener("beforeunload", (e) => {
  if (app.globalDirty || app.serverDirty) {
    e.preventDefault();
    e.returnValue = "";
  }
});
bootstrap().catch((e) => message(e.message, true));
setInterval(async () => {
  if (!app.user || document.hidden) return;
  try {
    await loadState();
    if (app.view !== "settings") {
      render();
    }
  } catch (e) {
    message(e.message, true);
  }
}, 10000);
