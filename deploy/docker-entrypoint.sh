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
  exec su-exec onefrog:onefrog "$@"
fi
exec "$@"
