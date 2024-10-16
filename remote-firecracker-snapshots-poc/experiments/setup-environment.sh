#!/bin/bash

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
ARCH=$(uname -m)  # Detect architecture (x86_64 or aarch64)

cd $DIR &> /dev/null

# RELEASE=$(basename $(curl -fsSLI -o /dev/null -w  %{url_effective} https://github.com/firecracker-microvm/firecracker/releases/latest))
RELEASE="v1.4.1"
RELEASE_DIR="release-$RELEASE-$ARCH"
FIRECRACKER_BINARY="firecracker-$RELEASE-$ARCH"


echo "Checking if you have Firecracker..."
if [ ! -f $RELEASE_DIR/$FIRECRACKER_BINARY ]; then
    wget https://github.com/firecracker-microvm/firecracker/releases/download/$RELEASE/$FIRECRACKER_BINARY.tgz
    tar -vzxf $FIRECRACKER_BINARY.tgz
    rm $FIRECRACKER_BINARY.tgz
fi
echo "Checking if you have Firecracker... done!"

echo "Checking if you have a Linux kernel image..."
if [ ! -f hello-vmlinux.bin ]; then
    curl -fsSL -o hello-vmlinux.bin https://s3.amazonaws.com/spec.ccfc.min/img/hello/kernel/hello-vmlinux.bin
fi
echo "Checking if you have a Linux kernel image... done!"

echo "Checking if you have a base rootfs..."
if [ ! -f hello-rootfs.ext4 ]; then
    curl -fsSL -o hello-rootfs.ext4 https://s3.amazonaws.com/spec.ccfc.min/img/hello/fsfiles/hello-rootfs.ext4
fi
echo "Checking if you have a base rootfs... done!"

cd - &> /dev/null
