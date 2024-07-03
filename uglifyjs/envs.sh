#!/bin/bash -eux

USER_ID="$(id -u)"
export USER_ID
GROUP_ID="$(id -g)"
export GROUP_ID
export NODEJS_VERSION="${NODEJS_VERSION:-14.15.3}"
export UGLIFYJS_VERSION="${UGLIFYJS_VERSION:-3.18.0}"
