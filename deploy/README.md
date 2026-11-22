# Docker Compose + 现有 Nginx 容器

服务器只需要 Linux 和 Docker Engine，不需要 Git、Go、Node.js 或项目源码。
需要 Docker Compose v2.20 或更新版本。下面的命令在服务器的 root Shell 中运行；
普通账号先执行 `sudo -i`。Compose 和 Nginx 文件只管理容器、网络、域名和证书入口；
监控规则、代理信任、通知、账号等日常参数仍通过网页保存到内部数据库，不使用 `.env`。

访问路径：浏览器 / 探针 → 域名 HTTPS → 现有 Nginx 容器 → 专用 Docker 网络 → 面板。
面板没有宿主机端口映射，也不使用 host 网络，公网 IP 的 `18080` 端口不提供面板访问。

## 镜像如何生成

- 提交到 `main`：测试通过后更新 `ghcr.io/fjiangming/system_monitor:latest`。
- 每次发布同时生成 `sha-完整40位提交号` 标签，便于按提交选择版本。
- 推送 `v*` 标签，例如 `v0.2.0`：发布同名镜像标签，不改变 `latest`。
- GitHub Actions 支持手动运行；只有 `main` 或 `v*` 标签会发布。
- Pull Request 和其他分支只验证，不发布。测试、三平台编译、AMD64 和 ARM64
  原生镜像与 Compose/Nginx HTTPS 集成测试全部通过后才执行发布。

一个标签包含 `linux/amd64`、`linux/arm64` 两种架构，Docker 自动选择。
发布使用 Actions 自带的 `GITHUB_TOKEN`，不需要另建镜像推送密钥，也不连接服务器。

构建状态：[GitHub Actions](https://github.com/fjiangming/system_monitor/actions)。
镜像页面：[GitHub Packages](https://github.com/users/fjiangming/packages/container/package/system_monitor)。

### 首次拉取权限

本仓库镜像已验证支持公开、免登录拉取，正常部署不需要 GitHub Token。

以下仅适用于以后将包改为私有，或在自己的账号下新建包的情况：GHCR 新建包可能默认
私有，源码仓库公开不代表包一定公开。需要免登录拉取时，在镜像页面的 Package settings
中将包可见性改为 Public；这是包的设置，后续构建不需要重复操作。

保持私有时，用有该包读取权限、包含 `read:packages` 的 GitHub Personal Access Token
（classic）登录。以下命令会交互询问密码，在 Password 中输入 Token：

```sh
docker login ghcr.io -u fjiangming
```

不要把 Token 写入仓库、网页配置或镜像。GitHub Actions 内置 Token 仅在构建期间使用，
不能拿来作为服务器的长期登录凭据。若拉取出现 `denied`，先检查上述包权限与登录状态。

## 准备部署文件

镜像已包含 Compose、Nginx 示例和探针安装文件。服务器只拉镜像并导出部署文件：

```sh
docker pull ghcr.io/fjiangming/system_monitor:latest
mkdir -p /opt/system-monitor
monitor_export=$(docker create ghcr.io/fjiangming/system_monitor:latest)
docker cp "$monitor_export:/usr/local/share/system-monitor/deploy/." /opt/system-monitor/
docker rm -v "$monitor_export"
cd /opt/system-monitor
```

后续更新镜像无需再次导出或覆盖自己调整过的 Compose/Nginx 文件。

## 创建专用网络和数据卷

```sh
docker network inspect system-monitor-proxy >/dev/null 2>&1 || docker network create system-monitor-proxy
docker volume create system-monitor-data
docker network inspect system-monitor-proxy --format '{{range .IPAM.Config}}{{println .Subnet}}{{end}}'
```

记录最后输出的专用网络 CIDR，例如 `172.23.0.0/16`，初始化页面会用到。
只把面板和负责反向代理的 Nginx 加入此网络，避免信任其他业务容器。

## 将现有 Nginx 接入网络

以下 `nginx` 替换为已有 Nginx 的实际容器名。可立即连接，不必为此重启 Nginx：

```sh
docker network connect system-monitor-proxy nginx
```

如果提示已经连接，无需重复操作。为保证 Nginx 重建后仍有此网络，在 **Nginx 自己的**
Compose 文件中追加以下网络，保留原有服务、挂载和全部网络：

```yaml
services:
  nginx:
    networks:
      - default          # 这里保留你原有的所有网络
      - monitor-proxy

networks:
  monitor-proxy:
    external: true
    name: system-monitor-proxy
```

Nginx 必须使用桥接网络；host 网络容器不能加入此网络。Compose 中的网络名可以不同，
但外部网络的 `name` 必须一致。面板在该网络上的固定 DNS 别名是 `system-monitor`。

## 配置域名、HTTPS 和拒绝 IP 访问

1. 将域名的 A/AAAA 记录指向服务器，准备与该域名匹配的有效 TLS 证书。
2. 将 `nginx-monitor.conf.example` 中的 `monitor.example.com` 替换为实际域名，
   证书路径替换为 **Nginx 容器内** 的证书路径。将文件放入现有 Nginx 的 `conf.d` 挂载目录，
   文件名使用 `monitor.conf`。
3. 参照 `nginx-default-deny.conf.example` 设置默认虚拟主机：HTTP 返回 444，HTTPS
   拒绝握手。已有 `default_server` 时修改或合并原默认块，不要重复定义。
   保留其他业务域名对应的 `server` 块。`ssl_reject_handshake` 需要 Nginx 1.19.4+。
4. 检查后重载 Nginx：

```sh
docker exec nginx nginx -t
docker exec nginx nginx -s reload
```

不清楚配置的宿主机挂载目录时，先查看：

```sh
docker inspect nginx --format '{{range .Mounts}}{{println .Source "->" .Destination}}{{end}}'
```

域名 HTTP 请求会跳转 HTTPS。IP、未知 Host 和不匹配的 TLS SNI 不会进入监控面板。
Nginx 使用 Docker DNS 动态解析后端，面板更新后即使容器 IP 改变也会重新连接。
代理会覆盖客户端提交的转发头；不要把示例改成信任任意客户端的 `X-Forwarded-For`。

## 启动和初始化面板

```sh
cd /opt/system-monitor
docker compose config --quiet
docker compose up -d --wait
docker compose logs --tail 30 monitor
```

访问 `https://你的监控域名`。填写日志中的初始化码、管理员账号和密码；在“反向代理设置”
中确认对外地址，并将受信任代理范围改为上面记录的 **专用网络 CIDR**。
系统会先验证一次性初始化码，再将管理员和代理信任一起保存到数据库。
默认显示当前 Nginx 的单个 IP，也可以直接接入；使用专用网络 CIDR 可避免 Nginx
重建更换 IP 后需要重新调整信任。

初次创建的会话即使用 Secure Cookie，支持正常登录和来源校验。之后代理信任仍在
“系统设置 → 服务设置”修改。外部 HTTPS 由 Nginx 终止，面板保持默认的“反向代理”模式。

Compose 不包含 `ports`，不挂载 Docker Socket；面板以 UID/GID `10001:10001` 运行，
只读根文件系统、移除 Linux capabilities，业务数据只写入数据卷，临时文件使用限额内存盘。
日志自动轮转。
容器内部监听保持 `0.0.0.0:18080`，**不能改成 `127.0.0.1`**，否则另一个容器中的
Nginx 无法连接。外部访问端口由 Nginx 管理，不需要在网页改内部监听端口。

数据库、配对加密密钥、备份和内部迁移记录都保存在命名卷 `system-monitor-data`。
数据卷声明为 external，Compose 重建或 `down` 不会删除它。不要执行
`docker volume rm system-monitor-data`。
容器中的初始数据目录是 `/var/lib/system-monitor/data`；如需在网页迁移数据目录，
目标仍应位于 `/var/lib/system-monitor/` 挂载范围内，例如
`/var/lib/system-monitor/data-v2`，且不能已存在或位于当前数据目录内。
这样重建容器后原始重定位记录与新数据仍在同一个卷中。

建议面板在另一台小服务器运行。面板和业务同机时，整机断电或失联期间不能自行发送告警。

## 从旧 docker run 方式迁移

如果已按旧文档使用 host 网络部署，先创建上述网络，在原网页设置里将受信任代理范围
加入此网络 CIDR、对外地址改为 HTTPS 域名，并保留内部监听 `0.0.0.0:18080` 和代理模式。
创建一次成功备份，再停止并移除旧容器，使用 Compose 启动：

```sh
docker stop --time 30 system-monitor
docker rm system-monitor
cd /opt/system-monitor
docker compose up -d --wait
```

Compose 显式使用同一个 `system-monitor-data` 卷，不会重新初始化账号或探针。
旧数据里的内置 HTTPS、其他端口或其他监听地址也会保留，迁移前需在网页调整并应用。

## 安装被监控主机的探针

探针在宿主机作为 systemd 服务运行，读取宿主的资源、Docker Unix Socket、systemd 和
备份报告。不要直接把面板容器当成宿主探针，也不需要给面板容器增加特权或挂载主机根目录。

在每台被监控服务器拉取同一个镜像，再从镜像复制已经编译好的程序与安装文件。
`docker create` 仅创建导出容器，不启动面板；镜像会自动匹配服务器架构。

```sh
docker pull ghcr.io/fjiangming/system_monitor:latest
monitor_bundle=$(mktemp -d)
monitor_export=$(docker create ghcr.io/fjiangming/system_monitor:latest)
docker cp "$monitor_export:/usr/local/bin/system-monitor" "$monitor_bundle/system-monitor"
docker cp "$monitor_export:/usr/local/share/system-monitor/deploy" "$monitor_bundle/deploy"
docker rm -v "$monitor_export"
sh "$monitor_bundle/deploy/install.sh" agent "$monitor_bundle/system-monitor"
rm -r "$monitor_bundle"
cd /var/lib/system-monitor/agent
```

`docker rm -v` 只清理由上述临时导出容器自动生成的匿名卷，与面板的命名卷无关。
安装步骤不会启动尚未接入的探针。

在面板“服务器与探针”新增服务器，保存后生成一次性接入命令。
保持上面的工作目录，执行网页命令，将开头的 `./system-monitor` 替换为
`/usr/local/bin/system-monitor`。接入成功后执行：

```sh
systemctl enable --now system-monitor-agent.service
systemctl status system-monitor-agent.service --no-pager
journalctl -u system-monitor-agent.service -n 30 --no-pager
```

网页“监控对象”中选择容器和逐容器重启权限，设置通知渠道并发送测试通知。
探针主动连接面板，不开放探针入站端口。Docker 只通过本地 Unix Socket 访问，
不开放 Docker TCP API，不接受任意远程 Shell 命令。

## 更新面板

先在网页“数据与保留”创建一次监控数据备份，将成功备份另存到异机。
确认要更新的 Actions 成功后执行：

```sh
cd /opt/system-monitor
docker image tag "$(docker inspect --format '{{.Image}}' "$(docker compose ps -q monitor)")" system-monitor:rollback
docker compose pull
docker compose up -d --wait
docker compose logs --tail 30 monitor
```

先完成下载；下载失败时现有服务继续运行。`docker compose up -d` 自动重建镜像变化的
面板容器，不重建现有 Nginx，Docker DNS 自动刷新。单独 `restart` 不会切换到新镜像。
管理员、探针接入关系、通知凭据和网页设置随数据卷保留。
网页可用后检查服务器在线状态、告警和通知。

如需固定版本，修改 `compose.yaml` 的 `image`，选择已发布的 `:v版本号` 或
`:sha-完整提交号` 标签。
要精确固定镜像内容，可使用 Actions 发布摘要中的
`ghcr.io/fjiangming/system_monitor@sha256:镜像摘要`。

## 更新探针

面板镜像升级不会自动替换宿主机探针。新版托管探针支持在服务器详情或
“系统设置 → 服务器与探针”点击“更新探针”：管理员确认后，探针通过原有连接
领取独立更新任务，从面板下载对应架构的程序，校验大小、SHA-256、Linux 架构和
构建信息，再原子替换程序并重新连接。更新期间继续发送心跳；目标版本重新
上报采样后才显示成功。版本是否一致以程序摘要判断，不依赖可能相同的版本号。

Docker 面板镜像内置 Linux AMD64 和 ARM64 更新包，更新目标固定为当前面板
随附的程序；不会从请求指定的 URL 下载，也不接受脚本、任意命令或 SSH 密码。
更新使用探针已有的身份、代理和经过证书校验的 HTTPS 连接；HTTP 仅允许本机
回环地址用于开发。关闭证书校验或仅以 `agent run` 启动时，不支持面板更新。
离线、状态过期、架构不支持、版本一致或有操作进行中时，面板显示具体原因。
同一服务器的更新与容器操作互斥；更新记录和操作者写入数据库与操作审计。

旧探针不认识更新协议，需先手动安装一次新版托管服务。先升级面板到目标镜像，
再以 root 执行下列步骤（使用与面板相同的镜像标签或摘要）：

```sh
docker pull ghcr.io/fjiangming/system_monitor:latest
monitor_bundle=$(mktemp -d)
monitor_export=$(docker create ghcr.io/fjiangming/system_monitor:latest)
docker cp "$monitor_export:/usr/local/bin/system-monitor" "$monitor_bundle/system-monitor"
docker cp "$monitor_export:/usr/local/share/system-monitor/deploy" "$monitor_bundle/deploy"
docker rm -v "$monitor_export"
systemctl stop system-monitor-agent.service
sh "$monitor_bundle/deploy/install.sh" agent "$monitor_bundle/system-monitor"
systemctl start system-monitor-agent.service
rm -r "$monitor_bundle"
journalctl -u system-monitor-agent.service -n 30 --no-pager
```

探针的本地身份仍在 `/var/lib/system-monitor/agent/agent-data`，不需要重新接入。
新服务由 `/usr/local/bin/system-monitor agent supervise` 托管，实际采集程序在
`/var/lib/system-monitor/agent/bin/system-monitor`。后续面板更新只替换采集程序，
不改 systemd、Nginx、防火墙或业务容器。原有 systemd 权限限制仍然保留。
原生 Linux 面板可提供自身架构的程序；提供其他架构时，发行包应在面板程序旁的
`agents/` 目录随附 `system-monitor-linux-amd64`、`system-monitor-linux-arm64`。

校验或下载失败时旧程序继续运行。稳定的托管进程在新版无法启动、启动后退出，
或 2 分钟内未恢复采样时，校验并恢复旧程序，再向面板回报失败。
最近一次成功更新前的程序保存在 `bin/system-monitor.previous-更新任务ID`；
下一次更新成功后清理更旧的备份。回退要求现有程序和备份的摘要均与更新记录
一致；文件被外部替换或磁盘损坏时停止覆盖并保留现场。若面板更新后变得不可达，
即使新版本身正常也会触发回退，此时应先恢复面板连接。
暂时停止期间面板可能显示探针失联；需要时先在网页开启维护窗口。

## 回退与备份

保留了上述 `system-monitor:rollback` 镜像时，将 Compose 中的 `image` 临时改为
`system-monitor:rollback`，执行 `docker compose up -d --pull never --wait`。
不要在完成验证前清理该回退镜像。代码降级能否直接读取数据取决于版本间的数据库兼容性；
如升级涉及不兼容迁移，应同时恢复升级前的配对备份。

网页备份包含一致的 `monitor.db` 和配对 `identity.key`。异机保存示例：

```sh
# 将末尾的目录名替换为网页显示的成功备份目录；迁移过数据目录时同步替换路径。
docker compose cp monitor:/var/lib/system-monitor/data/backups/manual-实际目录 ./monitor-backup
```

不要直接复制正在写入的 `monitor.db` 作为一致备份，也不要把不同批次的密钥和数据库混合。
恢复时停止面板，将同一次成功备份的两个文件放入新卷的空数据目录，
设置目录与文件归属为 `10001:10001`，再用该卷启动；不要带入旧 WAL / SHM 文件。

忘记密码时，在停止面板后通过相同卷运行本机恢复命令，再启动原面板：

```sh
docker compose stop monitor
docker compose run --rm --no-deps monitor recover-access
docker compose up -d --wait
```

恢复命令应使用与当前面板相同版本的镜像。记录控制台显示的临时密码，登录后在网页修改。

如果是代理范围误设导致无法登录，可先停止常驻面板，临时执行
`docker compose run --rm --no-deps -p 127.0.0.1:18080:18080 monitor serve`，
再通过 SSH 本地端口转发访问网页，修正服务设置。临时入口只绑定服务器回环地址。
操作完成后停止临时容器，再执行正常的 `docker compose up -d --wait`；不编辑内部数据库。
