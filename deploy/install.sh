#!/bin/sh
set -eu
mode=${1:-}
binary=${2:-./system-monitor}
case "$mode" in hub|agent) ;; *) printf '%s\n' 'Usage: sudo sh deploy/install.sh hub|agent /path/to/system-monitor'; exit 2;; esac
[ "$(id -u)" = 0 ] || { printf '%s\n' 'Run as root.'; exit 1; }
[ -f "$binary" ] || { printf '%s\n' 'Binary does not exist.'; exit 1; }
deploy_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
install -m 0755 "$binary" /usr/local/bin/system-monitor
if [ "$mode" = hub ]; then
 if ! id system-monitor >/dev/null 2>&1; then useradd --system --home-dir /var/lib/system-monitor/hub --shell /usr/sbin/nologin system-monitor; fi
 install -d -m 0700 -o system-monitor -g system-monitor /var/lib/system-monitor/hub
 install -m 0644 "$deploy_dir/system-monitor.service" /etc/systemd/system/system-monitor.service
 systemctl daemon-reload
 systemctl enable --now system-monitor.service
 printf '%s\n' 'Hub: http://SERVER_IP:18080' 'Initialization code: sudo journalctl -u system-monitor.service -n 30 --no-pager'
else
 install -d -m 0700 /var/lib/system-monitor/agent
 install -d -m 0700 /var/lib/system-monitor/agent/bin
 install -m 0755 "$binary" /var/lib/system-monitor/agent/bin/system-monitor
 install -m 0644 "$deploy_dir/system-monitor-agent.service" /etc/systemd/system/system-monitor-agent.service
 systemctl daemon-reload
 printf '%s\n' 'In the hub, create a server and generate its enrollment command.' 'Run that command from /var/lib/system-monitor/agent, using /usr/local/bin/system-monitor.' 'Then: sudo systemctl enable --now system-monitor-agent.service'
fi
