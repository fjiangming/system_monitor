#!/bin/sh
set -eu

image=${1:-system-monitor:ci}
package_dir=$(mktemp -d)
container="system-monitor-check-$$"
volume="$container-data"
cleanup() {
    docker rm -fv "$container" >/dev/null 2>&1 || true
    docker volume rm "$volume" >/dev/null 2>&1 || true
    rm -rf "$package_dir"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

docker volume create "$volume" >/dev/null
start_hub() {
    docker run -d --name "$container" \
        -p 127.0.0.1::18080 \
        --mount "type=volume,src=$volume,dst=/var/lib/system-monitor" \
        "$image" >/dev/null
    port=$(docker port "$container" 18080/tcp)
    port=${port##*:}
    attempt=0
    while [ "$attempt" -lt 30 ]; do
        if curl -fsS "http://127.0.0.1:$port/healthz"; then
            return
        fi
        attempt=$((attempt + 1))
        sleep 1
    done
    docker logs "$container"
    return 1
}

start_hub
[ "$(docker exec "$container" id -u)" = 10001 ]
docker exec "$container" test -s data/monitor.db
docker cp "$container:/var/lib/system-monitor/data/identity.key" "$package_dir/identity.before"
docker cp "$container:/usr/local/bin/system-monitor" "$package_dir/system-monitor"
docker cp "$container:/usr/local/share/system-monitor/deploy" "$package_dir/deploy"
"$package_dir/system-monitor" version
sh -n "$package_dir/deploy/install.sh"
for asset in install.sh system-monitor.service system-monitor-agent.service compose.yaml nginx-monitor.conf.example nginx-default-deny.conf.example README.md; do
    cmp "deploy/$asset" "$package_dir/deploy/$asset"
done

docker stop --time 30 "$container" >/dev/null
docker rm "$container" >/dev/null
start_hub
docker exec "$container" test -s data/monitor.db
docker cp "$container:/var/lib/system-monitor/data/identity.key" "$package_dir/identity.after"
cmp "$package_dir/identity.before" "$package_dir/identity.after"
printf '%s\n' 'Image startup, non-root access, agent export and volume reuse passed.'
