# 轻巡 · 独立系统监控

轻量、自托管的服务器与容器监控服务。Go 单程序，内置网页和 SQLite；手机浏览器直接查看。运行时不需要 Node.js 或外部数据库，监控参数全部在网页和内部数据库中管理。Compose 与 Nginx 文件只负责部署入口。

- Linux 主机 CPU、内存、磁盘、inode、在线状态与采样趋势。
- Docker 容器发现、运行 / 健康状态、重启次数、手动重启、最近日志。
- systemd、HTTP 检查，以及备份报告、超时和备份后服务恢复检查。
- 飞书、企业微信、Bark、SMTP、通用 Webhook，告警合并、重试和恢复通知。
- 管理员 / 只读账号、逐容器权限、数据库重启确认、操作审计。
- 八类网页设置、凭据加密、数据保留、容量限制、数据库与密钥配对备份。

## 运行

服务器部署推荐使用 Docker Compose 接入已有 Nginx 容器，详见[域名 HTTPS 部署、探针安装与更新](deploy/README.md)。面板不映射宿主机端口，Nginx 拒绝 IP 和未知域名访问。
服务器无需拉取源码；提交到 `main` 后测试通过即发布到 GHCR，支持 AMD64 和 ARM64。

从仓库编译，需要 Go 1.25 或更新版本：

```sh
git clone https://github.com/fjiangming/system_monitor.git
cd system_monitor
go build -trimpath -o system-monitor .
./system-monitor serve
```

Windows 使用 `go build -o system-monitor.exe .` 和 `system-monitor.exe serve`。Windows 支持监控面板，采集探针支持 Linux。

直接运行程序时默认监听 `0.0.0.0:18080`，供本机开发验证；服务器部署使用 Compose 和 Nginx 域名入口，不开放面板端口。首次启动在控制台显示初始化码，在网页中输入该码，创建至少 12 字节密码的管理员账号。

默认数据目录为启动目录中的 `data/`，程序自动生成内部数据库和加密密钥。首次启动可用 `--data /absolute/path` 指定存储位置，之后的日常参数均在网页修改。

推荐将面板放在另一台小服务器上，将探针作为被监控主机的 systemd 服务运行。这样业务容器全部停止时仍可观察；主机完全失联时面板仍可发告警。同一台主机安装也能发现容器故障，但不能在主机断电或网络断开时自行发送通知。没有使用浏览器后台轮询代替服务端告警。

## 接入 Linux 服务器

1. 在“系统设置 → 服务器与探针”创建服务器，保存设置并生成一次性接入命令。
2. 将对应架构的同一程序放到 Linux 主机，在探针的固定工作目录执行网页生成的命令。接入码有效期 10 分钟，只能使用一次，换取永久探针身份。
3. 执行 `sudo ./system-monitor agent run`。探针主动向面板连接，不需要开放入站端口。
4. 在“监控对象”选择需要监控的容器，分别开启允许重启的容器。自动发现默认开启，新容器自动纳管默认关闭，新容器没有重启权限。

普通参数、采样间隔、代理、TLS 校验、Docker Socket、HTTP 检查和备份检查均由网页下发。探针会保存最近连接参数，重启后仍可连接。自签名证书与代理的首次接入参数由网页生成，不必手写配置。重新接入前停止旧探针；使用新的有效接入命令可替换本地身份。

长期运行可用仓库附带的 systemd 服务：

```sh
# 安装并启动面板；会建立专用的低权限系统用户
sudo sh deploy/install.sh hub ./system-monitor

# 在被监控服务器安装探针服务；此步骤暂不启动探针
sudo sh deploy/install.sh agent ./system-monitor
cd /var/lib/system-monitor/agent
# 在这里执行网页生成的接入命令，将 ./system-monitor 换成 /usr/local/bin/system-monitor
sudo systemctl enable --now system-monitor-agent.service
```

Docker Socket 通常需要 root 或 Docker 组权限，等同高权限主机访问。探针仅执行固定类型的读取和容器重启，不接受远程 Shell、任意命令、文件上传或 Docker TCP 地址。业务容器不会因告警而自动重启。

## 网页设置

| 分类 | 可调整内容 |
| --- | --- |
| 服务器与探针 | 名称、分组、地址备注、启停、采样 / 失联 / 轮询间隔、代理、TLS 校验、接入 / 撤销凭据 |
| 监控对象 | Unix Socket、自动发现、逐容器监控 / 重启 / 数据库标记、磁盘、systemd、HTTP 检查与执行位置 |
| 告警规则 | 连续失败、CPU / 内存持续阈值、磁盘 GiB / inode、重启次数窗口、重复提醒、维护时长 |
| 备份检查 | 服务名、报告路径、计划 / 截止时间、执行时长、报告年龄、恢复健康要求 |
| 通知渠道 | 飞书 / 企业微信 / Bark / SMTP / Webhook、凭据、合并窗口、超时、重试、恢复与操作回执 |
| 账号与权限 | 账号、角色、密码、IP 范围、会话、登录锁定、重启 / 日志开关、确认、冷却与等待时间 |
| 服务设置 | 站点名、外部 URL、监听 IP / 端口、时区、代理信任、HTTP 超时、操作并发、匿名汇总、TLS 证书 |
| 数据与保留 | 数据目录、各类保留期、运行日志限额、数据库限额、磁盘预留、监控数据备份 |

保存后普通设置直接生效，探针设置在下次轮询同步。更改系统或服务器设置会取消尚未派发的重启任务，需重新确认；已经派发的操作不能保证撤回。

监听地址、端口、HTTPS 和数据目录在保存后点击“应用服务设置”。正在执行容器操作时禁止应用。失败会恢复原监听参数，网页显示失败状态。修改端口 / HTTPS 后需使用新地址重连，并同步调整外部防火墙或代理的对应入口。

数据迁移目标必须是尚不存在的绝对目录，不能位于当前数据目录之内。程序暂停服务，将一致性数据库、配对密钥、运行日志和已有备份迁移后重开。原目录保留内部重定位记录供下次启动寻找新目录；保持首次启动工作目录及原内部目录。没有需要人工编辑的定位文件。服务进程必须拥有目标目录权限；systemd 面板用户默认不能写入 `/root`。

“地址备注”不用于 SSH；服务器的连接由探针建立。“对外访问地址”用于生成接入命令。HTTP 检查不跟随重定向，预期的跳转应显式设置为对应状态码。探针侧关联容器的 HTTP 检查参与重启后健康验证；面板侧 HTTP 检查用于独立告警。

忘记管理员密码或误设 IP 限制时，先停止面板，在原工作目录以同一服务用户运行 `system-monitor recover-access`，再启动服务。该命令仅允许独占本地数据目录，会重置一个管理员密码、清除 IP 限制和旧会话，在本机显示临时密码并记录审计；业务数据、探针和通知凭据保留。登录后在网页更改密码。systemd 安装可在停止服务后进入 `/var/lib/system-monitor/hub`，使用 `sudo -u system-monitor /usr/local/bin/system-monitor recover-access`。

## 重启与告警语义

重启前检查在线状态、采样时效、全局与逐容器权限、不可变容器 ID、名称、数据库确认、冷却期及并发。相同容器不能同时存在两个未完成的重启。

接收请求后立即返回操作编号。探针持久记录操作开始后才调用 Docker，同一编号不会重复执行。进程中断或请求结果丢失会标为“结果未确认”，不会自动重试重启。必须人工核对最新状态后再发起新操作。

只有 Docker 健康检查和已关联的探针侧 HTTP 检查全部通过，才显示“检查通过”。未配置这些检查时仅显示“已启动 · 未验健康”；超时显示失败，不把 Docker 接收命令等同业务恢复。读取日志限制为最近 100 行、32 KiB，不持续跟随输出；超过限额会明确失败。

连续失败仅按新观测计数，重复处理旧样本不会加速触发。CPU / 内存还需满足持续时间。确认告警会停止重复提醒，但不会伪造恢复；维护窗口保留异常状态、抑制通知。探针失联或采样过期时不沿用旧快照声称健康。

通知通过持久队列发送，网络回包丢失时平台可能已收到消息，重试可能重复送达。修改系统设置后旧版本尚未发送的通知会取消，避免发往旧凭据。通知失败显示在事件记录。当前支持单一通知渠道。

## 备份检查

探针只读取备份执行结果，不执行、修改或停止业务备份。兼容此前在线备份生成的报告：

```json
{
  "completed": true,
  "services_healthy": true,
  "finished_at": "2026-11-21T03:20:00+08:00"
}
```

网页可填写 `sub2api-online-backup.service` 及 `/root/syncthing/backups/vps-online-*.tar.zst.report.json` 等实际路径。默认不启用，需根据服务器确认。使用最新修改的报告；缺少报告、报告无效、超期、服务失败、执行过久或未恢复健康均可告警。时间窗口支持跨午夜，按面板配置的时区判断。

监控服务自身的备份是独立功能：保存 SQLite 一致快照与匹配的 `identity.key`，通过检查后原子发布到内部 `backups/auto-*` 或 `backups/manual-*` 目录，不递归备份业务文件或旧备份。保留份数与可用空间检查会限制占用。

恢复时停止面板，将同一成功备份目录里的 `monitor.db` 和 `identity.key` 一起放入一个新的空目录，以 `system-monitor serve --data /restore/path` 启动。不要合并旧 WAL / SHM。缺少配对密钥时程序拒绝静默生成新密钥；通知和探针身份依赖该密钥。请将成功备份另外保存到异机，网页中的本机备份不等于异地灾备。

数据库容量主要限制 SQLite 主库，WAL、备份和运行日志单独占空间。在容量压力或剩余空间低于预留值时优先停止历史采样、清理旧记录；当前状态仍尝试保存。磁盘已满或底层存储不可写仍可能阻断写入，应将面板放在业务主机之外。

## Docker Compose 部署（推荐）

```sh
docker pull ghcr.io/fjiangming/system_monitor:latest
mkdir -p /opt/system-monitor
monitor_export=$(docker create ghcr.io/fjiangming/system_monitor:latest)
docker cp "$monitor_export:/usr/local/share/system-monitor/deploy/." /opt/system-monitor/
docker rm -v "$monitor_export"
```

当前镜像支持公开免登录拉取；若以后改为私有，按[部署文档](deploy/README.md)登录 GHCR。
按部署文档创建专用 Docker 网络和数据卷，将已有 Nginx 容器接入网络，配置域名、证书和默认拒绝虚拟主机后，在 `/opt/system-monitor` 执行 `docker compose up -d --wait`。
只通过域名 HTTPS 初始化；首次页面可直接保存代理信任，账号和代理配置一起验证并入库。

镜像同时包含宿主机探针程序和 systemd 安装文件，可直接通过 `docker cp` 导出安装。
服务器更新只需 `docker compose pull && docker compose up -d --wait`，数据卷自动保留；探针程序单独更新。
首次接入、更新、固定版本和回退的完整命令见[部署文档](deploy/README.md)。

## 开发与验证

```sh
go vet ./...
go test ./...
go test -race ./...                  # 需要支持 race 的本地 C 工具链
npm ci                              # 仅浏览器测试需要 Node
npx playwright install chromium
npm run test:browser
SYSTEM_MONITOR_DOCKER_TEST=1 go test -run TestRealDockerRestartAndLogs -v
```

浏览器回归启动真实的隔离面板、真实 SQLite / API，使用合成探针报告验证 UI。真实 Docker 集成仅在 Linux Docker 环境运行，会创建并清理专用测试容器，不操作业务容器。CI 运行 race、Linux 采集、真实 Docker 重启、浏览器回归和三平台编译，二进制产物位于 GitHub Actions 的 Artifacts。AMD64 和 ARM64 原生镜像测试验证非 root 启动、数据卷重用和探针包导出；Compose/Nginx 集成验证端口隔离、域名 HTTPS 初始化与登录、安全 Cookie、IP/Host/SNI 拒绝、其他虚拟主机兼容、伪造转发头拦截及容器更换 IP 后自动恢复。全部通过后，`main` 和 `v*` 标签自动发布多架构 GHCR 镜像。CI 不连接服务器部署，也不创建 Release。
