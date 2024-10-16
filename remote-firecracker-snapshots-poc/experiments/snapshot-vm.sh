#!/bin/bash


DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"

if [ -z "$1" ]; then
    echo "Syntax: ./snapshot-vm.sh <vm ip>"
    echo "Please use a free IP 172.18.[0,255].[3,255]. The IP will be used to create a firecracker directory and to route requests to the vm."
    exit 0
fi

# VM ip accessible to the outside (unique).
PUBLIC_VM_IP=$1

# VM directory.
VM_DIR=$DIR/$PUBLIC_VM_IP/root

# VM socket.
VM_SOCKET=$VM_DIR/firecracker.socket

# Snapshot file paths (files insire the chroot).
VM_SNAP_FILE=snapshot_file
VM_SNAP_MEM=mem_file

echo $VM_SNAP_FILE
echo $VM_SNAP_MEM

sudo curl --unix-socket $VM_SOCKET -i \
    -X PATCH "http://localhost/vm" \
    -d "{ \"state\": \"Paused\" }"

sudo curl --unix-socket $VM_SOCKET -i \
    -X PUT "http://localhost/snapshot/create" \
    -d "{
        \"snapshot_type\": \"Full\",
        \"snapshot_path\": \"$VM_SNAP_FILE\",
        \"mem_file_path\": \"$VM_SNAP_MEM\"
    }"

sudo curl --unix-socket $VM_SOCKET -i \
    -X PATCH "http://localhost/vm" \
    -d "{ \"state\": \"Resumed\" }"
