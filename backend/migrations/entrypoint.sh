#!/bin/sh
set -eu
if [ "$#" -gt 0 ]; then
    exec migrate "$@"
fi
exec migrate -path=/migrations -database="$DATABASE_URL" up
