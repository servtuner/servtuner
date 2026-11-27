# Sourced by the container test scripts: `source labeled-containers.sh <name>`.
# Every container a script starts carries $run_label and cleanup removes by label. A list kept in a
# variable would be lost: containers are often started inside $(...) subshells.
run_label="servtuner.$1=$(date +%s)-$$"

remove_run_containers() {
  docker ps -aq --filter "label=$run_label" | xargs -r docker rm -f >/dev/null 2>&1 || true
}
