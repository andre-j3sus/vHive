#!/bin/bash

CONFIGS=$ROOT/configs/demux-snapshotter

sudo cp $CONFIGS/config.toml /etc/demux-snapshotter/

sudo mkdir -p /var/lib/demux-snapshotter