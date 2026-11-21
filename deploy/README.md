# 从 GitHub 镜像部署

服务器只需要 Linux 和 Docker Engine，不需要 Git、Go、Node.js 或项目源码。
下面的命令在服务器的 root Shell 中运行；普通账号先执行 `sudo -i`。
不使用 Compose、`.env` 或运行参数配置文件，日常设置仍在网页和内部数据库中管理。

## 镜像如何生成

- 提交到 `main`：测试通过后更新 `ghcr.io/fjiangming/system_monitor:latest`。
- 每次发布同时生成 `sha-完整40位提交号` 标签，便于按提交选择版本。
- 推送 `v*` 标签，例如 `v0.2.0`：发布同名镜像标签，不改变 `latest`。
- GitHub Actions 支持手动运行；只有 `main` 或 `v*` 标签会发布。
- Pull Request 和其他分支只验证，不发布。测试、三平台编译、AMD64 和 ARM64
  原生镜像测试全部通过后才执行发布。

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

## 首次启动面板

确认对应提交的 Actions 已成功，并安装好 Docker 后执行：

```sh
docker pull ghcr.io/fjiangming/system_monitor:latest
docker volume create system-monitor-data
docker run -d --name system-monitor \
  --restart unless-stopped \
  --network host \
  --log-driver local \
  --mount type=volume,src=system-monitor-data,dst=/var/lib/system-monitor \
  ghcr.io/fjiangming/system_monitor:latest
docker logs --tail 30 system-monitor
```

访问 `http://服务器IP:18080`，使用日志中的首次初始化码创建管理员账号。
在网页“服务设置”填写对外访问地址，并配置 HTTPS 或现有反向代理。
管理入口的防火墙权限按实际访问范围开放。

使用 host 网络，所以没有 `-p` 端口映射；网页更改监听端口后会直接使用新端口，
需同时调整防火墙或代理。面板以 UID/GID `10001:10001` 运行，不挂载 Docker Socket。
`local` 日志驱动自动轮转容器输出；应用自身的日志保留在网页设置。

数据库、配对加密密钥、备份和内部迁移记录都保存在命名卷 `system-monitor-data`。
更新时始终挂载同一个卷。不要执行 `docker volume rm system-monitor-data`。
容器中的初始数据目录是 `/var/lib/system-monitor/data`；如需在网页迁移数据目录，
目标仍应位于 `/var/lib/system-monitor/` 挂载范围内，例如
`/var/lib/system-monitor/data-v2`，且不能已存在或位于当前数据目录内。
这样重建容器后原始重定位记录与新数据仍在同一个卷中。

建议面板在另一台小服务器运行。面板和业务同机时，整机断电或失联期间不能自行发送告警。

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
# 先完成下载；下载失败时现有服务继续运行。
docker pull ghcr.io/fjiangming/system_monitor:latest

# 保留当前运行版本，供需要时回退。
docker image tag "$(docker inspect --format '{{.Image}}' system-monitor)" system-monitor:rollback
docker stop --time 30 system-monitor
docker rm system-monitor
docker run -d --name system-monitor \
  --restart unless-stopped \
  --network host \
  --log-driver local \
  --mount type=volume,src=system-monitor-data,dst=/var/lib/system-monitor \
  ghcr.io/fjiangming/system_monitor:latest
docker logs --tail 30 system-monitor
```

`docker restart` 只重启原容器，不会切换到新下载的镜像，更新必须重建容器。
管理员、探针接入关系、通知凭据和网页设置随数据卷保留。
网页可用后检查服务器在线状态、告警和通知。

如需固定版本，把以上命令中的 `:latest` 换为 `:v0.2.0` 或对应 `:sha-完整提交号`。
要精确固定镜像内容，可使用 Actions 发布摘要中的
`ghcr.io/fjiangming/system_monitor@sha256:镜像摘要`。

## 更新探针

面板镜像升级不会替换宿主机探针。先拉取目标版本并导出，再停止探针覆盖程序：

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
暂时停止期间面板可能显示探针失联；需要时先在网页开启维护窗口。

## 回退与备份

保留了上述 `system-monitor:rollback` 镜像时，可停止并删除当前面板容器，
使用相同的 `docker run` 参数，将镜像换成 `system-monitor:rollback` 后启动。
不要在完成验证前清理该回退镜像。代码降级能否直接读取数据取决于版本间的数据库兼容性；
如升级涉及不兼容迁移，应同时恢复升级前的配对备份。

网页备份包含一致的 `monitor.db` 和配对 `identity.key`。异机保存示例：

```sh
# 将末尾的目录名替换为网页显示的成功备份目录；迁移过数据目录时同步替换路径。
docker cp system-monitor:/var/lib/system-monitor/data/backups/manual-实际目录 ./monitor-backup
```

不要直接复制正在写入的 `monitor.db` 作为一致备份，也不要把不同批次的密钥和数据库混合。
恢复时停止面板，将同一次成功备份的两个文件放入新卷的空数据目录，
设置目录与文件归属为 `10001:10001`，再用该卷启动；不要带入旧 WAL / SHM 文件。

忘记密码时，在停止面板后通过相同卷运行本机恢复命令，再启动原面板：

```sh
docker stop --time 30 system-monitor
docker run --rm \
  --mount type=volume,src=system-monitor-data,dst=/var/lib/system-monitor \
  "$(docker inspect --format '{{.Image}}' system-monitor)" recover-access
docker start system-monitor
```

恢复命令应使用与当前面板相同版本的镜像。记录控制台显示的临时密码，登录后在网页修改。
