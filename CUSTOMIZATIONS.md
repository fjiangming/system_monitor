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

## 2026-11-21: GitHub image publication and source-free deployment

Authorized scope: implement automatic GitHub container builds and publication so
servers can deploy and update from images without checking out source. Registry
publication is included; no production connection or server deployment is run.

- `.github/workflows/ci.yml`: publish GHCR images only for `main` and `v*` refs,
  after the existing verification and binary builds plus native AMD64/ARM64
  image tests pass. Use the built-in scoped `GITHUB_TOKEN`, OCI source/revision
  labels, `latest` on main only, full-commit tags and explicit version tags.
  Multi-platform builds use Buildx, cross-compilation and shared build caches;
  concurrent superseded runs for the same ref are cancelled. After pushing,
  inspect the image by registry digest, require both Linux platform entries
  and retain the actual registry manifest as an Actions artifact.
- `Dockerfile`: cross-compile for the requested Linux architecture and package
  the existing systemd installer and units alongside the binary. Keep the hub
  non-root with its original persistent path and no Docker Socket access.
- `scripts/image-check.sh`: verify real native container startup, UID 10001,
  SQLite creation, executable host-agent export, exact packaged installer/unit
  content and preservation of the identity key across container replacement.
  All test containers, volumes and export files are isolated and cleaned.
- `README.md`, `deploy/README.md`: document GHCR permissions, source-free hub
  deployment, host-agent extraction/enrollment/update, image tags and digests,
  pull-before-stop updates, persistent volumes, rollback, paired backups and
  offline access recovery. Runtime operator settings remain in the UI/database;
  no Compose, environment configuration or remote execution is introduced.

Local `go vet ./...`, `go test -count=1 ./...`, Actionlint 1.7.7, Linux shell
syntax checks and `git diff --check` passed. The local Docker daemon is
unavailable, so native image checks run on GitHub's Linux runners rather than
being claimed as local tests. Successful publication is gated on those checks;
the actual published workflow and registry manifests must be verified before
delivery.

The first workflow (Actions run 44912701282, commit 05a3a81) passed Linux
race/real-Docker/browser verification, all three binary builds, both native
architecture image checks (including agent export and persistent identity
reuse), and GHCR publication. An anonymous registry request returned HTTP 200
for `latest`, with both Linux architecture entries and index digest
`sha256:3467c8703db78b072a3d45befe89826b7b469adc2c113d4d689c3d7fe35a1c30`.
No package visibility or other GitHub access settings were changed. Registry
inspection is included in the final workflow so it also verifies pulls of the
published index rather than relying only on a successful build export.

## 2026-11-21: Compose behind an existing Nginx container

Authorized scope: optimize container deployment with Docker Compose, make the
panel available through a domain and Nginx rather than the server IP, and retain
source-free image deployment. The user confirmed an existing Nginx container.
No server is connected or deployed by this change.

- `deploy/compose.yaml`: an independent hub service with no host port mapping or
  host networking, a dedicated external proxy network and stable DNS alias, the
  existing external data volume, health checks, graceful shutdown, bounded logs,
  a read-only root filesystem, bounded temporary memory storage, dropped
  capabilities and no Docker Socket.
- `deploy/nginx-monitor.conf.example`, `deploy/nginx-default-deny.conf.example`:
  domain HTTPS proxying, HTTP redirection, IP/unknown-host/SNI rejection, Docker
  DNS re-resolution after hub replacement, and replacement of client-supplied
  forwarding headers. Existing named business virtual hosts are preserved;
  default-deny blocks must be merged with any existing default server.
- `auth.go`, `api.go`: first-run proxy configuration is authorized by the same
  one-use setup code and committed atomically with the administrator. Validate
  the proxy network, current peer, public origin and cross-origin request before
  use; rejected or replayed setup and normal login cannot change proxy trust.
  Existing installations keep their defaults and saved settings. Normal origin
  and CSRF checks remain enforced; proxy HTTPS sessions receive Secure cookies.
- `web/app.js`, `web/app.css`: optional first-run public URL and trusted-proxy
  fields, with an observed peer hint for an untrusted HTTPS reverse proxy.
  Daily configuration remains in the existing web settings and database.
- `Dockerfile`, `scripts/image-check.sh`: include and verify the complete deploy
  package in each image so the server exports deployment files without source.
- `api_test.go`, `scripts/browser-check.cjs`, `package.json`: verify atomic
  first-run authorization and rejection cases, trusted HTTPS cookies/login,
  initialization form behavior and narrow-screen layout, plus syntax checks.
- `scripts/compose-check.cjs`, `scripts/compose-test.yaml`,
  `.github/workflows/ci.yml`: native AMD64/ARM64 integration uses the actual
  Compose manifest, hub image and Nginx, with an isolated TLS certificate,
  network and volume. Check private ports, HTTPS setup/login, header forgery,
  origin protection, IP/Host/SNI rejection, other virtual-host access, persisted
  sessions and automatic DNS recovery after forcing a different container IP.
- `README.md`, `deploy/README.md`: source-free export, joining an existing proxy,
  domain/certificate/default-host setup, UI proxy trust, migration from the old
  host-network container, Compose updates/rollback/recovery and persistent data.
  Compose/Nginx configuration is deployment infrastructure explicitly requested
  here; application operator settings remain managed through the UI/database.

Local Go vet/tests, JavaScript syntax, Actionlint, production/test Compose
configuration and whitespace checks passed. Browser acceptance passed all prior
flows plus proxy setup and 320 px initialization layout; desktop/mobile setup
screenshots were inspected. The local Docker daemon remains unavailable. Native
Compose/Nginx checks are required in both architecture jobs before publication,
and the actual workflow and published registry revision are verified at delivery.
The host-agent installation remains native systemd so host resource, systemd and
backup observations retain their existing semantics and permissions.

The first native Nginx run passed domain/IP/SNI rejection, other virtual hosts,
HTTPS initialization, Secure cookies and origin checks. Its address-change
fixture then hit Docker's requirement that a static endpoint IP needs explicit
IPAM. The test now recreates its own empty network with the same automatically
selected subnet declared explicitly before reserving the old address. Production
network creation is unchanged; failed checks prevented image publication.

## 2026-11-22: Network traffic monitoring and anomaly notifications

Authorized scope: add traffic observations and configurable notifications for
possible DDoS or bandwidth anomalies. No server deployment, firewall changes,
Cloudflare account changes or business-application modifications are included.

- `model.go`, `network.go`, `network_linux.go`, `collector_linux.go`: per-server,
  database-managed interface selection, inbound/outbound Mbps and inbound packet
  thresholds, and a separate sustained period. Read Linux counters directly;
  auto-select and deduplicate IPv4/IPv6 default-route interfaces. Calculate rates
  with monotonic elapsed time, rebaseline on counter resets, interface identity
  changes and collection gaps, and bound selected/reported interfaces and rates.
  Thresholds default to disabled; existing server records retain their settings.
- `monitor.go`: reuse new-observation failure counting and existing durable
  notifications, acknowledgment, maintenance, repetition and recovery for traffic
  anomalies. Missing/partial rates interrupt pending traffic thresholds, retain
  active alerts without claiming recovery, and raise an availability warning
  when thresholds are enabled but traffic data remains unavailable.
- `store.go`: add a backward-compatible network history table attached to the
  existing samples with cascading retention, transactional writes, nullable old
  or unknown rates, and validation of agent traffic observations.
- `web/app.js`, `web/app.css`: add per-server traffic settings, overview speeds,
  detailed rates and interface drop/error rates, and receive/transmit trends with
  unknown samples shown as gaps. Reload trends when returning to server details
  from settings. Use the existing responsive UI and icons.
- `network_test.go`, `integration_linux_test.go`, `scripts/browser-check.cjs`:
  cover parsing, routing, rate calculation, resets, interface replacement/gaps,
  legacy settings/database upgrade, history retention, threshold duration and
  deduplication, missing-data behavior, notification queue integration, and real
  hub/browser configuration, charts and responsive layouts.
- `README.md`: document measurements, threshold tuning, paired hub/agent
  upgrades, detection latency, external availability checks and upstream DDoS
  protection. Monitoring does not itself mitigate or identify an attack.

Verification passed: Windows `go vet ./...`, `go test -count=1 ./...`, JavaScript
syntax checks, Linux AMD64 test-binary compilation and the complete test suite
executed in local WSL Ubuntu (including real Linux network counters and Unix
Socket agent integration), Linux ARM64 cross-build, and real hub/browser
acceptance of traffic configuration propagation, known/unknown history, chart
reloads and all nine settings groups at 1440/1060/390/320 px. Desktop traffic and
mobile traffic/settings screenshots were visually inspected. The opt-in real
Docker restart test was not enabled; no Docker behavior changes are included.
Whitespace checks passed. No production service or external protection settings
were changed, and no deployment or publication was performed.

## 2026-11-22: Automatic traffic baselines without operator thresholds

Authorized scope: replace manual traffic thresholds with automatic learning and
anomaly notifications so operators need not estimate bandwidth or packet rates.
This supersedes the manual-threshold behavior in the preceding traffic entry.
No deployment or changes to business applications or external defenses are run.

- `model.go`, `web/app.js`, `web/app.css`: enable automatic detection by default,
  remove numeric traffic settings, retain an on/off control and optional interface
  selection, and show learning progress and analysis state with normal reference
  rates. Existing server records inherit automatic mode without editing settings.
- `network_auto.go`, `monitor.go`, `store.go`: learn a robust range from accepted
  observations of the same interface set, with a minimum of 30 observations over
  30 minutes and a bounded 24-hour reference. Combine median, upper percentile,
  MAD and idle-host noise floors. Require three new high observations over at
  least 60 seconds, use hysteresis for recovery, and persist frozen incident
  references so sustained anomalies cannot train themselves into normality.
  Separate learning markers cascade with existing history retention. Maintenance
  does not train the baseline; configured, observed backup activity suppresses
  only a new outbound anomaly, while inbound and existing anomalies remain checked.
  Unknown rates preserve active incidents and interrupt pending alerts. Existing
  notification, acknowledgment, maintenance and recovery semantics are reused.
- `network_auto_test.go`, `network_test.go`, `scripts/browser-check.cjs`: verify
  learning, noise floors, isolated spikes, hysteresis, backup handling, duplicate
  observations, notification/recovery, interface changes, unknown samples,
  maintenance exclusions and a persistent incident after history expiry/restart.
  Browser checks verify default automatic mode, no numeric traffic inputs,
  configuration propagation, on/off behavior and actual learning status.
- `README.md`: replace threshold-tuning instructions with automatic behavior,
  learning time, algorithm details and limitations, including initial learning
  during an attack and legitimate large changes in demand. Notification channel
  credentials and the notification switch still need the operator's selection.

Verification passed: Windows `go test -count=1 ./...`, `go vet ./...`, JavaScript
syntax checks, Linux AMD64 test compilation and the complete test suite in WSL
Ubuntu, and Linux ARM64 cross-build. Real hub/browser acceptance passed automatic
defaults, learning status, absence of numeric traffic inputs, saved on/off and
interface configuration, charts and all nine settings groups at four widths.
Desktop monitoring and mobile automatic-settings screenshots were visually
inspected. Whitespace checks passed. The opt-in real Docker test was not run;
Docker behavior was not changed. No publication or deployment was performed.

## 2026-11-22: Publish automatic traffic monitoring to GitHub

The user explicitly requested a GitHub submission. Commit and push the complete
traffic collection, automatic baselines, notifications, responsive UI, tests and
documentation to the repository's existing `main` branch. Preserve the unrelated
pre-existing local server-deployment ledger entry without including it in this
feature commit. The local verification recorded above applies to this source.
The existing GitHub workflow verifies Linux race/real-Docker/browser behavior,
platform builds and native images before publishing AMD64/ARM64 GHCR images.
Verify its result after pushing. No production server deployment is authorized
or performed by this publication request.
