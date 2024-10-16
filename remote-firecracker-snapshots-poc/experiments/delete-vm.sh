#!/bin/bash


DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"

if [ -z "$2" ]; then
    echo "Syntax: ./delete-vm.sh <clone id> <vm ip>"
    echo "Clone id should an integer higher than zero which is not being used by another clone vm."
    echo "Please use a free IP 172.18.[0,255].[3,255]. The IP will be used to create a firecracker directory and to route requests to the vm."
    exit 0
fi

# ID of the VM clone. Used to prepare internal ips.
CLONE_ID=$1

# VM ip accessible to the outside (unique).
PUBLIC_VM_IP=$2

# ID of the vm (based on the public ip).
VM_ID=$(echo $PUBLIC_VM_IP | tr . -)

./stop-vm.sh $CLONE_ID $PUBLIC_VM_IP

# Delete vm directory.
sudo rm -r $DIR/$PUBLIC_VM_IP
