#!/bin/bash
set -e

IMAGE=registry.lestak.sh/humun-chainmgr
TAG=$GIT_COMMIT

docker build -f Dockerfile \
    -t $IMAGE:$TAG \
    .

docker push $IMAGE:$TAG

sed "s,$IMAGE:.*,$IMAGE:$TAG," devops/k8s/*.yaml | kubectl apply -f -
