"use strict";

const assert = require("node:assert/strict");
const cp = require("node:child_process");
const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const os = require("node:os");
const path = require("node:path");

if (process.platform !== "linux") throw Error("Run this integration check on Linux with Docker.");
const root = path.resolve(__dirname, "..");
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "monitor-compose-"));
const project = "monitor-compose-" + process.pid;
const network = project + "-proxy";
const volume = project + "-data";
const nginx = project + "-nginx";
const blocker = project + "-old-address";
const env = {
  ...process.env,
  MONITOR_TEST_IMAGE: process.argv[2] || "system-monitor:ci",
  MONITOR_TEST_VOLUME: volume,
  MONITOR_TEST_NETWORK: network,
};
const composeArgs = ["compose", "-f", "deploy/compose.yaml", "-f", "scripts/compose-test.yaml", "-p", project];
const docker = (...args) => cp.execFileSync("docker", args, {cwd: root, env, encoding: "utf8", stdio: "pipe"}).trim();
const compose = (...args) => docker(...composeArgs, ...args);
const inspect = (name) => JSON.parse(docker("inspect", name))[0];
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
let monitor, tlsPort, httpPort, ca, cookie = "";

function request(route, body, options = {}) {
  const plain = options.plain || false;
  const data = body === undefined ? undefined : JSON.stringify(body);
  const port = plain ? httpPort : tlsPort;
  return new Promise((resolve, reject) => {
    const req = (plain ? http : https).request({
      hostname: "127.0.0.1", port, path: route, method: data === undefined ? "GET" : "POST",
      ...(plain ? {} : {servername: options.sni ?? "monitor.example.com", ca}),
      agent: false,
      headers: {
        Host: "monitor.example.com:" + port,
        ...(cookie ? {Cookie: cookie} : {}),
        ...(data === undefined ? {} : {"Content-Type": "application/json", "Content-Length": Buffer.byteLength(data), Origin: "https://monitor.example.com:" + tlsPort}),
        ...options.headers,
      },
    }, (res) => {
      let text = "";
      res.setEncoding("utf8");
      res.on("data", (chunk) => { text += chunk; });
      res.on("end", () => resolve({status: res.statusCode, headers: res.headers, text, json: () => JSON.parse(text)}));
    });
    req.setTimeout(7000, () => req.destroy(Error("HTTP request timed out")));
    req.on("error", reject);
    req.end(data);
  });
}

async function ready() {
  for (let attempt = 0; attempt < 30; attempt++) {
    try {
      const result = await request("/api/bootstrap");
      if (result.status === 200) return result.json();
    } catch {}
    await delay(1000);
  }
  throw Error("Nginx did not reach the monitor after DNS refresh");
}

function isolated() {
  const container = inspect(monitor);
  assert.equal(Object.keys(container.HostConfig.PortBindings || {}).length, 0, "hub published a host port");
  assert.equal(container.HostConfig.ReadonlyRootfs, true);
  assert.ok(container.HostConfig.CapDrop.includes("ALL"));
  assert.ok(container.NetworkSettings.Networks[network], "hub missing its dedicated proxy network");
  return container.NetworkSettings.Networks[network].IPAddress;
}

async function check() {
  const certDir = path.join(tmp, "certs");
  const confDir = path.join(tmp, "conf");
  fs.mkdirSync(certDir);
  fs.mkdirSync(confDir);
  cp.execFileSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
    "-subj", "/CN=monitor.example.com", "-addext", "subjectAltName=DNS:monitor.example.com,DNS:other.example.com",
    "-keyout", path.join(certDir, "privkey.pem"), "-out", path.join(certDir, "fullchain.pem")], {stdio: "pipe"});
  ca = fs.readFileSync(path.join(certDir, "fullchain.pem"));
  for (const file of ["nginx-monitor.conf.example", "nginx-default-deny.conf.example"]) {
    fs.copyFileSync(path.join(root, "deploy", file), path.join(confDir, file.replace(".example", "")));
  }
  fs.writeFileSync(path.join(confDir, "other.conf"), `server {
    listen 443 ssl;
    server_name other.example.com;
    ssl_certificate /etc/nginx/certs/monitor/fullchain.pem;
    ssl_certificate_key /etc/nginx/certs/monitor/privkey.pem;
    return 200 "other site";
  }\n`);
  docker("network", "create", network);
  const subnet = JSON.parse(docker("network", "inspect", network))[0].IPAM.Config[0].Subnet;
  // Explicit IPAM permits reserving the retired address to force a DNS change.
  docker("network", "rm", network);
  docker("network", "create", "--subnet", subnet, network);
  docker("volume", "create", volume);
  const rendered = JSON.parse(compose("config", "--format", "json"));
  assert.equal(Object.keys(rendered.services.monitor.ports || {}).length, 0);
  compose("up", "-d", "--wait", "--wait-timeout", "90");
  monitor = compose("ps", "-q", "monitor");
  const originalIP = isolated();
  const log = cp.spawnSync("docker", ["logs", monitor], {encoding: "utf8"});
  const code = (log.stdout + log.stderr).match(/首次初始化码（仅在本机显示）：\s+([\w-]+)/u)?.[1];
  assert.ok(code, "initialization code missing");
  docker("run", "-d", "--name", nginx, "--network", network,
    "-p", "127.0.0.1::80", "-p", "127.0.0.1::443",
    "--mount", "type=bind,src=" + confDir + ",dst=/etc/nginx/conf.d,readonly",
    "--mount", "type=bind,src=" + certDir + ",dst=/etc/nginx/certs/monitor,readonly",
    "nginx:stable-alpine");
  docker("exec", nginx, "nginx", "-t");
  tlsPort = Number(docker("port", nginx, "443/tcp").split(":").pop());
  httpPort = Number(docker("port", nginx, "80/tcp").split(":").pop());
  const initial = await ready();
  assert.equal(initial.setup, true);
  assert.equal(initial.proxyNeedsTrust, true);
  assert.equal(initial.proxyPeer, inspect(nginx).NetworkSettings.Networks[network].IPAddress);

  const redirect = await request("/api/bootstrap?check=redirect", undefined, {plain: true});
  assert.equal(redirect.status, 308);
  assert.equal(redirect.headers.location, "https://monitor.example.com/api/bootstrap?check=redirect");
  await assert.rejects(request("/", undefined, {plain: true, headers: {Host: "127.0.0.1"}}));
  await assert.rejects(request("/", undefined, {plain: true, headers: {Host: "unknown.example.com"}}));
  await assert.rejects(request("/", undefined, {sni: ""}));
  await assert.rejects(request("/", undefined, {sni: "unknown.example.com"}));
  await assert.rejects(request("/", undefined, {headers: {Host: "127.0.0.1"}}));
  await assert.rejects(request("/", undefined, {sni: "other.example.com"}));
  const other = await request("/", undefined, {sni: "other.example.com", headers: {Host: "other.example.com"}});
  assert.equal(other.status, 200);
  assert.equal(other.text, "other site");
  console.log("Nginx rejected IP, unknown host and mismatched SNI access; other virtual hosts stayed reachable.");

  const credentials = {username: "admin", password: "compose-test-password-only"};
  const proxy = {publicURL: "https://monitor.example.com:" + tlsPort, trustedProxies: subnet};
  const setup = {code, ...credentials, proxy};
  assert.equal((await request("/api/setup", {...setup, code: "invalid"})).status, 400);
  assert.equal((await request("/api/bootstrap")).json().setup, true);
  const initialized = await request("/api/setup", setup, {headers: {"X-Forwarded-Proto": "http", "X-Forwarded-For": "198.51.100.77"}});
  assert.equal(initialized.status, 200, initialized.text);
  const session = initialized.headers["set-cookie"][0];
  assert.match(session, /; Secure/);
  assert.match(session, /; HttpOnly/);
  assert.match(session, /SameSite=Strict/);
  cookie = session.split(";")[0];
  let csrf = initialized.json().csrf;
  let saved = (await request("/api/config")).json();
  assert.equal(saved.config.service.publicURL, proxy.publicURL);
  assert.equal(saved.config.service.trustedProxies, subnet);
  assert.equal((await request("/api/logout", {}, {headers: {Origin: "https://evil.example", "X-CSRF-Token": csrf}})).status, 403);
  assert.equal((await request("/api/login", {...credentials, proxy})).status, 400);

  compose("rm", "--stop", "--force", "monitor");
  docker("run", "-d", "--name", blocker, "--network", network, "--ip", originalIP, "nginx:stable-alpine", "sleep", "300");
  compose("up", "-d", "--wait", "--wait-timeout", "90");
  monitor = compose("ps", "-q", "monitor");
  assert.notEqual(isolated(), originalIP, "fixture failed to change the hub IP");
  const restored = await ready();
  assert.equal(restored.setup, false);
  assert.ok(restored.user.id, "session was lost after container replacement");
  const login = await request("/api/login", credentials);
  assert.equal(login.status, 200, login.text);
  assert.match(login.headers["set-cookie"][0], /; Secure/);
  cookie = login.headers["set-cookie"][0].split(";")[0];
  csrf = login.json().csrf;
  saved = (await request("/api/config")).json();
  saved.config.access.allowIPs = "198.51.100.77";
  const changed = await request("/api/config/save", {config: saved.config, revision: saved.revision, secrets: {}, clearSecrets: []}, {headers: {"X-CSRF-Token": csrf}});
  assert.equal(changed.status, 200, changed.text);
  const spoofed = await request("/api/config", undefined, {headers: {"X-Forwarded-For": "198.51.100.77"}});
  assert.equal(spoofed.status, 403, "forged forwarded IP bypassed the allowlist");
  console.log("Compose kept the hub private and persisted accounts, settings and sessions across an IP-changing replacement.");
  console.log("HTTPS setup/login, Secure cookies, CSRF, proxy header replacement and Docker DNS refresh passed.");
}

check().catch((error) => {
  console.error(error);
  for (const container of [monitor, nginx]) {
    if (container) {
      const result = cp.spawnSync("docker", ["logs", "--tail", "40", container], {encoding: "utf8"});
      process.stderr.write(result.stdout || "");
      process.stderr.write(result.stderr || "");
    }
  }
  process.exitCode = 1;
}).finally(() => {
  for (const args of [[...composeArgs, "down", "--remove-orphans"], ["rm", "-fv", nginx, blocker], ["volume", "rm", volume], ["network", "rm", network]]) {
    cp.spawnSync("docker", args, {cwd: root, env, stdio: "ignore"});
  }
  fs.rmSync(tmp, {recursive: true, force: true});
});
