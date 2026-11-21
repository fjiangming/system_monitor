/* Integration fixture: real hub, SQLite, HTTP API and browser; agent reports are isolated test data. */
"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const net = require("node:net");
const http = require("node:http");
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
  await agent("poll", { id, snapshot }, identity.token);
  await page.locator("[data-action=nav][data-view=monitor]").click();
  await page.locator("[data-action=refresh]").click();
  await page.locator("[data-action=detail]").first().click();
  await page.getByRole("heading", { name: "容器", exact: true }).waitFor();
  await overflow(page);
  await page.screenshot({
    path: path.join(artifacts, "desktop-monitor.png"),
    fullPage: true,
  });
  await page.locator("[data-action=edit-server]").click();
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
    await page.locator("[data-action=nav][data-view=monitor]").click();
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
    "Browser checks passed: setup, real API enrollment, permissions, restart receipts, database confirmation, notification, accounts, 8 settings groups at 4 widths, port apply, data migration and restart.",
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
