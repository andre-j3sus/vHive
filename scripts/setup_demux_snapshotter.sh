#!/bin/bash

set -e

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT="$( cd $DIR && cd .. && pwd)"
CONFIGS=$ROOT/configs/demux-snapshotter

sudo mkdir -p /etc/demux-snapshotter/

sudo cp $CONFIGS/config.toml /etc/demux-snapshotter/

sudo mkdir -p /var/lib/demux-snapshotter