# Maintenance Ledger

## 2026-11-21: Initial independent system monitor

Authorized scope: implement the approved responsive monitoring interface, manage
runtime settings in the browser, collect Linux and Docker status with a separate
agent, perform authenticated and audited container restarts, test the complete
service, and publish the source to fjiangming/system_monitor. No production
deployment or business application changes are authorized.

The initial implementation comprises the Go hub and agent, embedded web assets,
SQLite persistence and migrations, authentication and encrypted secrets, typed
configuration, monitoring and notification workers, installation/build assets,
documentation, and focused unit, integration, Docker, and browser tests. Detailed
verification and file inventory follow.

### File inventory and behavior

| Files | Initial implementation |
| --- | --- |
| `main.go`, `filelock_linux.go`, `filelock_windows.go` | Hub/agent command entry, single-instance locking, bounded runtime logs, live listener/TLS application with rollback, consistent data-directory migration and internal relocation records. |
| `model.go`, `store.go` | Validated web-managed settings, SQLite schema, encrypted credentials and key verification, CAS updates, one-use enrollment, durable immutable-target jobs, deduplication, retention/capacity bounds, atomic verified database/key backup bundles. |
| `auth.go`, `api.go` | First-run code, PBKDF2 passwords, hashed sessions, CSRF, proxy trust, role/IP controls, login lockout, last-admin guard, local offline access recovery, authenticated hub/agent APIs, embedded static UI and browser security headers. |
| `agent.go`, `docker.go`, `collector_linux.go`, `collector_windows.go`, `collector_other.go` | Linux host/Docker/systemd/HTTP/backup collection, Unix Socket Docker protocol, persistent agent identity, durable non-replayed commands, independent heartbeats during restart, bounded log reads, Docker/HTTP health verification. Windows hosts the hub but cannot run an agent. |
| `monitor.go`, `notify.go` | New-observation failure counting, offline/stale/host/container/unit/HTTP/backup alerts, maintenance/ack/recovery, separate bounded external HTTP probes, durable notification queue and Feishu/WeCom/Bark/SMTP/HMAC webhook delivery. |
| `web/index.html`, `web/app.js`, `web/app.css`, `web/icon.svg` | Responsive Chinese monitoring/settings/account interface, eight complete settings categories, manual restart confirmation and persistent receipts, first-run/login, mobile and dark layouts; no mocked production state or runtime CDN calls. |
| `web/lucide.min.js`, `web/lucide-LICENSE.txt`, `THIRD_PARTY_NOTICES.md` | Vendored Lucide 0.468.0 with original ISC notice and dependency attribution. |
| `store_test.go`, `api_test.go`, `docker_test.go`, `integration_linux_test.go`, `runtime_test.go`, `scripts/browser-check.cjs` | Storage/auth/API/job/alert/encryption/backup/migration tests; fake Docker protocol tests; Linux Unix Socket/collector/agent-loop/crash-recovery integration; opt-in real Docker restart/log integration; actual HTTPS rollback/restart verification; real hub/browser acceptance checks with isolated agent fixtures. |
| `go.mod`, `go.sum`, `package.json`, `package-lock.json` | Pinned Go runtime and development-only browser dependencies; no Node dependency for running the product. |
| `Dockerfile`, `.dockerignore`, `deploy/install.sh`, `deploy/*.service` | Optional hub image and Linux systemd installation assets, shipped without executing deployment. No Docker Socket mount for the hub. |
| `.github/workflows/ci.yml`, `.gitignore`, `.gitattributes`, `AGENTS.md`, `README.md`, `CUSTOMIZATIONS.md` | Verification/build gates, source hygiene, maintenance rules and Chinese operation/restore documentation. |

No business application is modified. No automatic business-container restart,
remote shell, Docker TCP exposure, production connection, deployment, image
publication or Release creation is included. Database-confirmed manual restart
is separately authorized per container. Started-without-healthcheck, healthy,
failed and uncertain outcomes remain distinct. Settings changes cancel queued
restarts; already dispatched actions cannot be reliably revoked.

### Verification before publication

Local Windows checks passed: `go vet ./...`, `go test -count=1 ./...`, JavaScript
syntax checks, Linux amd64/arm64 cross-builds and Linux test-binary compilation.
Browser acceptance passed against the real isolated hub, SQLite and APIs:
initialization, enrollment, permissions, queued/unchecked restart receipts,
database confirmation, actual local webhook test, viewer restrictions, all eight
settings groups at 1440/1060/390/320 px, dark theme, listener-port application,
data-directory migration and restart through the original bootstrap directory.
Desktop/mobile/dark screenshots were visually inspected. Temporary browser
processes and data are cleaned by the test harness.

This machine has CGO disabled and no available Docker daemon. Local race testing
was therefore not runnable, and no local test is represented as a real Docker
restart. GitHub CI supplies Linux race, real Docker, full agent-loop, browser,
image-build and three-platform build gates. CI run status must be verified on
the published commit before final delivery. Generated binaries, databases,
screenshots and test identities are excluded from source control.

The delivery uses a dedicated temporary checkout. After a verified GitHub push
and final checks, that exact checkout is removed at the user's request. Existing
user-owned checkouts, the originating repository and unrelated edits are kept.

### Final verification fixes

The first GitHub Linux run caught a missing explicit cancellation in a Linux-only
test's second agent lifecycle. The test now closes and waits for each lifecycle
and explicitly cancels both contexts. Final review also found that pending TLS
credential deletion could break rollback to an active HTTPS listener. Active
service metadata and encrypted active certificate copies now commit atomically,
remain separate from pending credentials, and are excluded from secret-status
responses. The active certificate remains in memory on apply failure and in the
database across process restarts. `runtime_test.go` verifies an actual trusted
HTTPS connection, occupied target port, cleared pending certificates, rollback,
and a second start from the same database.

The subsequent Linux race run passed the core, agent-loop and HTTPS tests, then
caught a real-Docker fixture issue: Docker's first image pull writes progress to
stderr, which had been combined with the container ID. The fixture now captures
stdout alone for the ID. Production collection uses the Docker HTTP API and is
unaffected by CLI pull output. The real-Docker check remains required in CI.

Real Docker then exposed a production transport timeout: Docker may spend longer
than ten seconds stopping a PID 1 process, but a fixed response-header timeout
was shorter than the authorized restart deadline. Restart POSTs now use their
configured command context; read-only Docker requests keep a separate ten-second
bound. Ambiguous failures still remain unknown and are never automatically
retried. The real container test verifies the complete stop/restart/health path.

Commit 530ffe4 passed Linux `go test -race -count=1 ./...`, including actual Docker
restart/health/logs, full agent lifecycle and HTTPS rollback. The remaining CI
failure was the floating runner upgrading to Ubuntu 26.04, unsupported by the
pinned Playwright release. All CI jobs now use supported Ubuntu 24.04 explicitly;
browser and build gates are retained.
