#!/bin/bash

VERSION=$1

REPO=failurepedia
NAME=promtun

if [ -z "$VERSION" ]; then
    echo "usage: $0 <version>"
    exit 1
fi

echo "===== BUILDING $NAME:$VERSION ====="
docker build --build-arg VERSION=$VERSION -t $REPO/$NAME:$VERSION .

echo "===== PUSHING $REPO/$NAME:$VERSION ====="
docker push $REPO/$NAME:$VERSION
