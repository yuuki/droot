#!/bin/bash

set -e

NAME="droot"

ROOT=$(dirname $0)/..

goxc -tasks='xc archive' -bc 'linux,!arm darwin' -d .
cp -p "$ROOT"/snapshot/linux_amd64/"$NAME" "$ROOT"/snapshot/"$NAME"_linux_amd64
cp -p "$ROOT"/snapshot/linux_386/"$NAME" "$ROOT"/snapshot/"$NAME"_linux_386
cp -p "$ROOT"/snapshot/darwin_amd64/"$NAME" "$ROOT"/snapshot/"$NAME"_darwin_amd64
cp -p "$ROOT"/snapshot/darwin_386/"$NAME" "$ROOT"/snapshot/"$NAME"_darwin_386
