#!/bin/sh
# Runs the server as the unprivileged "onefrog" user. When started as root
# (the default), it first makes sure the data volume — possibly created by an
# older image that ran as root — is owned by that user, then drops privileges.
set -eu
data="${OF_DATA_DIR:-/app/data}"
if [ "$(id -u)" = "0" ]; then
  mkdir -p "$data"
  if [ "$(stat -c %u "$data")" != "$(id -u onefrog)" ]; then
    chown -R onefrog:onefrog "$data"
  fi
  # The installer bind-mounts this directory. Own it so the unprivileged
  # server can drop an upgrade request. Do not create it: a directory that
  # exists only inside the container is not watched by the host.
  upgrade="${OF_UPGRADE_DIR:-}"
  case "$upgrade" in
    ""|*..*|*[!A-Za-z0-9._/-]*|/) ;;
    /*)
      if [ -d "$upgrade" ]; then
        chown onefrog:onefrog "$upgrade" || true
      fi
      ;;
  esac
  exec su-exec onefrog:onefrog "$@"
fi
exec "$@"
