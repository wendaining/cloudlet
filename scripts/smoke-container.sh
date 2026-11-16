#!/bin/sh
set -eu
image="${1:?需要容器镜像名称}"
container_id="$(docker run --detach --read-only --cap-drop=ALL --security-opt=no-new-privileges --memory=128m --cpus=1 --pids-limit=128 --publish 127.0.0.1::8080 --publish 127.0.0.1::8081 "$image")"
cleanup() {
  docker rm --force "$container_id" >/dev/null
}
trap cleanup EXIT HUP INT TERM
api_port="$(docker inspect --format '{{(index (index .NetworkSettings.Ports "8080/tcp") 0).HostPort}}' "$container_id")"
gateway_port="$(docker inspect --format '{{(index (index .NetworkSettings.Ports "8081/tcp") 0).HostPort}}' "$container_id")"
curl --retry 20 --retry-delay 1 --retry-connrefused --retry-all-errors --fail --silent --show-error "http://127.0.0.1:$api_port/readyz"
docker exec "$container_id" /app/cloudlet health
curl --fail --silent --show-error -H 'Host: demo.localhost' "http://127.0.0.1:$gateway_port/api/v1/version"
status="$(curl --silent --output /dev/null --write-out '%{http_code}' -H 'Host: unknown.localhost' "http://127.0.0.1:$gateway_port/healthz")"
test "$status" = 404
docker logs "$container_id"
docker stop --time 12 "$container_id" >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$container_id")" = 0
echo '容器启动、CLI、路由、未知域名和正常退出检查通过'
