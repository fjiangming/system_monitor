/* Integration fixture: real hub, SQLite, HTTP API and browser; agent reports are isolated test data. */
"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const net = require("node:net");
const http = require("node:http");
const { createHash } = require("node:crypto");
const { spawn, execFileSync } = require("node:child_process");
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright");
const root = path.resolve(__dirname, "..");
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "system-monitor-browser-"));
const binary = path.join(
  tmp,
  process.platform === "win32" ? "monitor.exe" : "monitor",
);
const dataDir = path.join(tmp, "data");
const artifacts = path.join(root, "artifacts", "browser");
fs.mkdirSync(artifacts, { recursive: true });
const password = "browser-test-password-only";
let child, browser, webhook;
let base;
const errors = [];
const delay = (ms) => new Promise((r) => setTimeout(r, ms));
async function freePort() {
  const s = net.createServer();
  await new Promise((r) => s.listen(0, "127.0.0.1", r));
  const p = s.address().port;
  await new Promise((r) => s.close(r));
  return p;
}
async function start() {
  child = spawn(binary, ["serve", "--data", dataDir], {
    cwd: tmp,
    windowsHide: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stderr = "";
  child.stderr.on("data", (b) => {
    stderr += b;
  });
  for (let i = 0; i < 100; i++) {
    if (child.exitCode != null) throw Error("hub exited: " + stderr);
    try {
      if ((await fetch(base + "/healthz")).ok) return;
    } catch {}
    await delay(100);
  }
  throw Error("hub did not start");
}
async function stop() {
  if (!child) return;
  const p = child;
  child = null;
  if (p.exitCode == null) {
    p.kill("SIGTERM");
    await new Promise((resolve) => {
      p.once("exit", resolve);
      setTimeout(resolve, 3000);
    });
  }
}
async function agent(pathname, body, token) {
  const r = await fetch(base + "/api/agent/" + pathname, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: "Bearer " + token } : {}),
    },
    body: JSON.stringify(body),
  });
  assert.equal(r.status, 200, "agent " + pathname);
  return r.json();
}
async function category(page, name) {
  const mobile = await page.locator("#category-select").isVisible();
  if (mobile) await page.locator("#category-select").selectOption(name);
  else
    await page.locator(`[data-action=category][data-category=${name}]`).click();
}
async function overflow(page) {
  const info = await page.evaluate(() => ({
    category: document.querySelector("#category-select")?.value,
    viewport: innerWidth,
    width: document.documentElement.scrollWidth,
    overflow: [...document.querySelectorAll("main *")]
      .filter(
        (x) =>
          x.getBoundingClientRect().right > innerWidth + 2 &&
          !x.closest(".table-scroll"),
      )
      .map((x) => ({
        tag: x.tagName,
        id: x.id,
        class: x.className,
        width: x.getBoundingClientRect().width,
      }))
      .slice(0, 6),
    offenders: [...document.querySelectorAll("h1,h2,label,button")]
      .filter(
        (x) =>
          x.getBoundingClientRect().width &&
          x.scrollWidth > x.clientWidth + 2 &&
          getComputedStyle(x).overflowX === "visible",
      )
      .map((x) => x.textContent.trim())
      .slice(0, 8),
  }));
  if (info.width > info.viewport + 1)
    await page.screenshot({
      path: path.join(
        artifacts,
        "overflow-" + info.category + "-" + info.viewport + ".png",
      ),
      fullPage: true,
    });
  assert.ok(info.width <= info.viewport + 1, JSON.stringify(info));
  assert.deepEqual(info.offenders, [], JSON.stringify(info));
}
async function check() {
  const port = await freePort();
  base = "http://127.0.0.1:" + port;
  const preparation = execFileSync(
    "go",
    ["test", "-run", "^TestPrepareBrowserData$", "-v", "."],
    {
      cwd: root,
      encoding: "utf8",
      env: {
        ...process.env,
        SYSTEM_MONITOR_PREPARE_DATA: dataDir,
        SYSTEM_MONITOR_PREPARE_PORT: String(port),
      },
      windowsHide: true,
    },
  );
  const code = preparation.match(/BROWSER_SETUP=([\w-]+)/)?.[1];
  assert.ok(code, "setup code generated");
  execFileSync("go", ["build", "-o", binary, "."], {
    cwd: root,
    stdio: "pipe",
    windowsHide: true,
  });
  const agentPackage = path.join(tmp, "agents", "system-monitor-linux-amd64");
  fs.mkdirSync(path.dirname(agentPackage), { recursive: true });
  execFileSync("go", ["build", "-trimpath", "-o", agentPackage, "."], {
    cwd: root, stdio: "pipe", windowsHide: true,
    env: { ...process.env, GOOS: "linux", GOARCH: "amd64", CGO_ENABLED: "0" },
  });
  await start();
  browser = await chromium.launch({
    headless: true,
    ...(process.env.CHROME_PATH
      ? { executablePath: process.env.CHROME_PATH }
      : {}),
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  const page = await context.newPage();
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("Failed to load resource"))
      errors.push(m.text());
  });
  await page.goto(base);
  await page.locator(".auth-proxy summary").click();
  await page.locator('[name="trustedProxies"]').fill("127.0.0.1");
  assert.equal(await page.locator('[name="publicURL"]').inputValue(), base);
  await page.screenshot({path: path.join(artifacts, "setup-proxy-desktop.png"), fullPage: true});
  await page.setViewportSize({width: 320, height: 800});
  await overflow(page);
  await page.screenshot({path: path.join(artifacts, "setup-proxy-mobile.png"), fullPage: true});
  await page.setViewportSize({width: 1440, height: 1000});
  await page.locator("[name=code]").fill(code);
  await page.locator("[name=username]").fill("admin");
  await page.locator("[name=password]").fill(password);
  await page.locator("#auth-form [type=submit]").click();
  await page.getByRole("heading", { name: "运行监控", exact: true }).waitFor();
  await page.getByRole("button", { name: "添加服务器" }).first().click();
  await page.locator("#f-name").fill("本地验收服务器");
  await page.locator("#f-region").fill("隔离测试");
  await page.locator("#settings-form [type=submit]").click();
  await page.getByRole("button", { name: "接入探针" }).click();
  const command = await page.locator("#enrollment-command").textContent();
  const id = command.match(/--id '([^']+)'/)[1],
    enrollment = command.match(/--code '([^']+)'/)[1];
  await page.locator("#dialog [data-action=close]").first().click();
  const identity = await agent("enroll", { id, code: enrollment });
  const webID = "a".repeat(64),
    dbID = "b".repeat(64);
  const snapshot = {
    hostname: "qa-linux",
    os: "linux",
    cores: 4,
    cpu: 18.4,
    memoryTotal: 8589934592,
    memoryUsed: 2147483648,
    uptime: 185000,
    disks: [
      { path: "/", total: 214748364800, free: 107374182400, inodeFree: 88 },
    ],
    network: [{
      name: "eth0", ready: true,
      receiveBytesPerSecond: 1250000, transmitBytesPerSecond: 250000,
      receivePacketsPerSecond: 4000, transmitPacketsPerSecond: 500,
      receiveDroppedPerSecond: 2, transmitDroppedPerSecond: 0,
      receiveErrorsPerSecond: 0, transmitErrorsPerSecond: 0,
    }],
    containers: [
      {
        id: webID,
        name: "web",
        image: "nginx:stable",
        state: "running",
        health: "none",
        startedAt: new Date().toISOString(),
        restarts: 0,
        database: false,
      },
      {
        id: dbID,
        name: "postgres",
        image: "postgres:17",
        state: "running",
        health: "healthy",
        startedAt: new Date().toISOString(),
        restarts: 1,
        database: true,
      },
    ],
    units: [],
    checks: [],
    backup: {
      successAt: 0,
      startedAt: 0,
      running: false,
      failed: false,
      servicesHealthy: false,
      error: "",
    },
    errors: [],
    at: 0,
    agentVersion: "test-agent",
  };
  await agent("poll", { id, snapshot: { ...snapshot, network: [{ name: "eth0", ready: false }] } }, identity.token);
  await agent("poll", { id, snapshot }, identity.token);
  await page.locator("[data-action=nav][data-view=monitor]").click();
  await page.locator("[data-action=refresh]").click();
  await page.locator("[data-action=detail]").first().click();
  await page.getByRole("heading", { name: "容器", exact: true }).waitFor();
  await page.locator("#network-trend .network-sample:not(.missing)").first().waitFor();
  assert.equal(await page.locator(".network-summary").getByText("10.00 Mbps", { exact: true }).count(), 1);
  assert.equal(await page.locator("#network-trend .missing").count(), 1);
  assert.ok((await page.locator("#network-trend .network-sample:not(.missing)").getAttribute("title")).includes("4000 包/秒"));
  await page.locator('[aria-label="流量监控"]').getByText(/^学习中/).waitFor();
  await overflow(page);
  await page.screenshot({
    path: path.join(artifacts, "desktop-monitor.png"),
    fullPage: true,
  });
  assert.equal(await page.locator('[data-action="agent-update"]').isDisabled(), true);
  await page.getByText("旧探针需先手动安装一次新版托管服务", { exact: true }).waitFor();
  snapshot.agent = { protocol: 1, arch: "amd64", revision: "previous", sha256: "1".repeat(64), updatable: true };
  await agent("poll", { id, snapshot }, identity.token);
  await page.locator("[data-action=refresh]").click();
  await page.locator("[data-action=agent-update]").click();
  await page.setViewportSize({ width: 320, height: 800 });
  await overflow(page);
  await page.screenshot({ path: path.join(artifacts, "agent-update-confirm-mobile.png"), fullPage: true });
  await page.locator("[data-action=confirm-agent-update]").click();
  await page.locator("#dialog").getByText("等待领取", { exact: true }).waitFor();
  const updateReply = await agent("poll", { id }, identity.token);
  assert.ok(updateReply.update);
  assert.equal(updateReply.job, null);
  assert.equal(updateReply.update.target.arch, "amd64");
  const packageResponse = await fetch(base + "/api/agent/update-download", {
    method: "POST", headers: { "Content-Type": "application/json", Authorization: "Bearer " + identity.token },
    body: JSON.stringify({ id, update: { id: updateReply.update.id } }),
  });
  assert.equal(packageResponse.status, 200);
  const packageBytes = Buffer.from(await packageResponse.arrayBuffer());
  assert.equal(createHash("sha256").update(packageBytes).digest("hex"), updateReply.update.target.sha256);
  await agent("update-status", { id, update: { id: updateReply.update.id, state: "restarting" } }, identity.token);
  snapshot.agent.sha256 = updateReply.update.target.sha256;
  snapshot.agent.revision = updateReply.update.target.revision;
  await agent("poll", { id, snapshot }, identity.token);
  await page.locator("#dialog").getByText("更新成功", { exact: true }).waitFor();
  await page.screenshot({ path: path.join(artifacts, "agent-update-success-mobile.png"), fullPage: true });
  await page.locator("#dialog [data-action=close]").first().click();
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.locator("[data-action=refresh]").click();
  assert.equal(await page.locator('[data-action="agent-update"]').isDisabled(), true);
  await page.screenshot({ path: path.join(artifacts, "agent-update-desktop.png"), fullPage: true });
  await page.locator("[data-action=edit-server]").click();
  await category(page, "network");
  assert.equal(await page.locator("#f-network-automatic").isChecked(), true);
  assert.equal(await page.locator("#settings-form input[type=number]").count(), 0);
  await page.locator(".network-options summary").click();
  await page.locator("#f-network-interfaces").fill("eth0");
  await page.locator("#settings-form [type=submit]").click();
  const networkConfig = (await agent("poll", { id }, identity.token)).config.network;
  assert.deepEqual(networkConfig, { interfaces: "eth0", automatic: true });
  await page.locator("#f-network-automatic").uncheck();
  await page.locator("#settings-form [type=submit]").click();
  assert.equal((await agent("poll", { id }, identity.token)).config.network.automatic, false);
  await page.locator("#f-network-automatic").check();
  await page.locator("#settings-form [type=submit]").click();
  await category(page, "targets");
  await page
    .getByRole("checkbox", { name: "web monitor", exact: true })
    .check();
  await page
    .getByRole("checkbox", { name: "web restart", exact: true })
    .check();
  await page
    .getByRole("checkbox", { name: "postgres monitor", exact: true })
    .check();
  await page
    .getByRole("checkbox", { name: "postgres restart", exact: true })
    .check();
  await page.locator("#settings-form [type=submit]").click();
  await page.locator("[data-action=nav][data-view=monitor]").click();
  await page
    .locator('[data-action=restart][data-container="' + webID + '"]')
    .click();
  await page.locator("#restart-form [name=remark]").fill("浏览器回归");
  await page.locator("#restart-form [type=submit]").click();
  await page.locator("#dialog").getByText("排队中", { exact: true }).waitFor();
  const leased = await agent("poll", { id }, identity.token);
  assert.equal(leased.job.containerID, webID);
  assert.equal(leased.job.action, "restart");
  assert.equal((await agent("poll", { id }, identity.token)).job, null);
  await agent(
    "result",
    {
      id,
      job: {
        ...leased.job,
        state: "unchecked",
        result: "容器已运行，未配置探针侧健康检查",
      },
    },
    identity.token,
  );
  await page
    .locator("#dialog")
    .getByText("已启动 · 未验健康", { exact: true })
    .waitFor();
  await page.locator("#dialog [data-action=close]").first().click();
  await page
    .locator('[data-action=restart][data-container="' + dbID + '"]')
    .click();
  await page.locator("#restart-form [name=confirmation]").fill("wrong");
  await page.locator("#restart-form [type=submit]").click();
  await page
    .locator("#dialog-status")
    .getByText("请输入数据库容器名确认")
    .waitFor();
  await page.locator("#dialog [data-action=close]").first().click();
  await page.locator("[data-action=nav][data-view=settings]").click();
  await category(page, "notifications");
  await page.locator("#f-notifications-channel").selectOption("webhook");
  let delivered = 0;
  webhook = http.createServer((req, res) => {
    delivered++;
    req.resume();
    res.setHeader("Content-Type", "application/json");
    res.end("{}");
  });
  await new Promise((r) => webhook.listen(0, "127.0.0.1", r));
  await page
    .locator("[data-secret=webhookEndpoint]")
    .fill("http://127.0.0.1:" + webhook.address().port);
  await page
    .locator("[data-secret=webhookSecret]")
    .fill("browser-webhook-secret");
  await page.locator("#settings-form [type=submit]").click();
  await page.locator("[data-action=test-notification]").click();
  await page.locator("#notice").getByText("测试通知已被通知平台接收").waitFor();
  assert.equal(delivered, 1);
  assert.equal(
    await page.locator("[data-secret=webhookSecret]").inputValue(),
    "",
  );
  assert.ok(
    !JSON.stringify(await page.evaluate(() => ({ ...localStorage }))).includes(
      "secret",
    ),
  );
  const categories = [
    "servers",
    "targets",
    "network",
    "alerts",
    "backup",
    "notifications",
    "access",
    "service",
    "storage",
  ];
  for (const width of [1440, 1060, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    for (const name of categories) {
      await category(page, name);
      await overflow(page);
    }
    await category(page, "targets");
    await page.screenshot({
      path: path.join(artifacts, "settings-" + width + ".png"),
      fullPage: true,
    });
    await category(page, "network");
    assert.equal(await page.locator("#f-network-automatic").isChecked(), true);
    assert.equal(await page.locator("#settings-form input[type=number]").count(), 0);
    await page.screenshot({
      path: path.join(artifacts, "network-settings-" + width + ".png"),
      fullPage: true,
    });
    await page.locator("[data-action=nav][data-view=monitor]").click();
    await page.locator("#network-trend .network-sample:not(.missing)").first().waitFor();
    await overflow(page);
    await page.screenshot({
      path: path.join(artifacts, "monitor-" + width + ".png"),
      fullPage: true,
    });
    await page.locator("[data-action=nav][data-view=settings]").click();
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await category(page, "access");
  await page.locator("[data-action=new-user]").click();
  await page.locator("#user-form [name=username]").fill("reader");
  await page
    .locator("#user-form [name=password]")
    .fill("browser-reader-password");
  await page.locator("#user-form [type=submit]").click();
  await page.locator("#dialog").waitFor({ state: "hidden" });
  const viewer = await browser.newContext({
    viewport: { width: 390, height: 844 },
  });
  const vp = await viewer.newPage();
  await vp.goto(base);
  await vp.locator("[name=username]").fill("reader");
  await vp.locator("[name=password]").fill("browser-reader-password");
  await vp.locator("#auth-form [type=submit]").click();
  await vp.getByRole("heading", { name: "运行监控", exact: true }).waitFor();
  assert.equal(await vp.locator("[data-view=settings]").count(), 0);
  await vp.locator("[data-action=detail]").first().click();
  assert.equal(await vp.locator("[data-action=agent-update]").count(), 0);
  const forbidden = await viewer.request.get(base + "/api/config");
  assert.equal(forbidden.status(), 403);
  await viewer.close();
  await category(page, "service");
  const newPort = await freePort();
  await page.locator("#f-service-listenPort").fill(String(newPort));
  await page.locator("#settings-form [type=submit]").click();
  await page
    .locator("#notice")
    .getByText("系统设置已保存，服务参数可点击应用")
    .waitFor();
  await page.locator("[data-action=apply]").click();
  base = "http://127.0.0.1:" + newPort;
  for (let i = 0; i < 80; i++) {
    try {
      if ((await fetch(base + "/healthz")).ok) break;
    } catch {}
    await delay(100);
  }
  await page.goto(base);
  await page.locator("[data-action=nav][data-view=settings]").click();
  await category(page, "storage");
  const moved = path.join(tmp, "moved-data");
  await page.locator("#f-storage-dataDirectory").fill(moved);
  await page.locator("#settings-form [type=submit]").click();
  await page
    .locator("#notice")
    .getByText("系统设置已保存，服务参数可点击应用")
    .waitFor();
  await page.locator("[data-action=apply]").click();
  await delay(1500);
  await page.reload();
  await page.locator("[data-action=nav][data-view=settings]").click();
  await category(page, "storage");
  await page.getByText(moved, { exact: true }).waitFor();
  await stop();
  await start();
  await page.reload();
  await page.locator("[data-action=nav][data-view=settings]").click();
  await category(page, "storage");
  await page.getByText(moved, { exact: true }).waitFor();
  await page.locator("[data-action=theme]").click();
  await overflow(page);
  await page.screenshot({
    path: path.join(artifacts, "dark-storage.png"),
    fullPage: true,
  });
  assert.deepEqual(errors, [], "browser runtime errors");
  console.log(
    "Browser checks passed: setup, real API enrollment, permissions, agent update confirmation/download/receipts, restart receipts, database confirmation, notification, accounts, traffic settings and charts, 9 settings groups at 4 widths, port apply, data migration and restart.",
  );
}
check()
  .catch((e) => {
    console.error(e.stack);
    process.exitCode = 1;
  })
  .finally(async () => {
    if (browser) await browser.close();
    await stop();
    if (webhook) await new Promise((r) => webhook.close(r));
    if (tmp.startsWith(path.join(os.tmpdir(), "system-monitor-browser-")))
      fs.rmSync(tmp, { recursive: true, force: true });
  });
