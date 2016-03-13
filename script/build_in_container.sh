#!/bin/bash

set -e

DOCKER_IMAGE_NAME="droot-build"
DOCKER_CONTAINER_NAME="droot-build-container"

if [[ $(docker ps -a | grep $DOCKER_CONTAINER_NAME) != "" ]]; then
  docker rm -f $DOCKER_CONTAINER_NAME 2>/dev/null
fi

docker build -t $DOCKER_IMAGE_NAME .

docker run --volume "$GOPATH"/src/:/go/src/ -e GOOS=$GOOS -e GOARCH=$GOARCH --name $DOCKER_CONTAINER_NAME $DOCKER_IMAGE_NAME "$@"

if [[ "$@" == *"clean"* ]] && [[ -d bin ]]; then
  rm -Rf bin
fi

docker cp $DOCKER_CONTAINER_NAME:/go/src/github.com/yuuki/droot/droot .
