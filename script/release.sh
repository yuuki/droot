#!/bin/bash

set -e

if [ -z "$1" ]; then
    echo "required patch/minor/major" 1>&2
    exit 1
fi

ROOT=$(dirname $0)/..
MAIN='cmd/droot'

# gobump
new_version=$(gobump "$1" -w -v "${MAIN}" | jq -r '.[]')
git add "${MAIN}/version.go"
git commit -m "Bump version $new_version"
git push origin master

# build release files
"$ROOT"/script/build_in_container.sh "$ROOT"/script/build_release.sh

# make github release draft
ghr --username yuuki --replace --draft "v$new_version" snapshot/
