#!/bin/bash

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
ARCH=$(uname -m)  # Detect architecture (x86_64 or aarch64)

if [ -z "$2" ]; then
    echo "Syntax: ./start-vm.sh <clone id> <vm ip>"
    echo "Clone id should be an integer higher than zero which is not being used by another clone vm."
    echo "Please use a free IP 172.18.[0,255].[3,255]. The IP will be used to create a firecracker directory and to route requests to the vm."
    exit 0
fi

# RELEASE=$(basename $(curl -fsSLI -o /dev/null -w  %{url_effective} https://github.com/firecracker-microvm/firecracker/releases/latest))
RELEASE="v1.4.1"
RELEASE_DIR="release-$RELEASE-$ARCH"
FIRECRACKER_BINARY="firecracker-$RELEASE-$ARCH"
JAILER_BINARY="jailer-$RELEASE-$ARCH"

# ID of the VM clone. Used to prepare internal ips.
CLONE_ID=$1

# VM ip accessible to the outside (unique).
PUBLIC_VM_IP=$2

# ID of the vm (based on the public ip).
VM_ID=$(echo $PUBLIC_VM_IP | tr . -)

# Create the directory structure for the VM
CHROOT_DIR=$DIR/$PUBLIC_VM_IP
mkdir -p $CHROOT_DIR/$FIRECRACKER_BINARY/$VM_ID/root/
touch $CHROOT_DIR/$FIRECRACKER_BINARY/$VM_ID/root/firecracker.log

# Create namespace only if it doesn't exist
sudo ip netns add ns$CLONE_ID


# Start the VM
sudo "$DIR/$RELEASE_DIR/$JAILER_BINARY" \
    --id $VM_ID \
    --exec-file "$DIR/$RELEASE_DIR/$FIRECRACKER_BINARY" \
    --uid 0 \
    --gid 0 \
    --netns /var/run/netns/ns$CLONE_ID \
    --chroot-base-dir $CHROOT_DIR \
    -- \
    --api-sock firecracker.socket \
    --log-path firecracker.log \
    --level Debug \
    --show-level \
    --show-log-origin
