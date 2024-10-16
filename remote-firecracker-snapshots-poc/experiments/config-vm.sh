#!/bin/bash

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
ARCH=$(uname -m)  # Detect architecture (x86_64 or aarch64)

if [ -z "$2" ]; then
    echo "Syntax: ./config-vm.sh <clone id> <vm ip> [<snapshot vm ip>]"
    echo "Clone id should be an integer higher than zero which is not being used by another clone vm."
    echo "Please use a free IP 172.18.[0,255].[3,255]. The IP will be used to create a firecracker directory and to route requests to the vm."
    echo "The snapshot vm ip will be used to find the vm directory where the snapshot resides."
    exit 0
fi

# RELEASE=$(basename $(curl -fsSLI -o /dev/null -w  %{url_effective} https://github.com/firecracker-microvm/firecracker/releases/latest))
RELEASE="v1.4.1"
FIRECRACKER_BINARY="firecracker-$RELEASE-$ARCH"


# ID of the VM clone. Used to prepare internal ips.
CLONE_ID=$1

# VM ip accessible to the outside (unique).
PUBLIC_VM_IP=$2

# Old VM from which we will restore. This argument is optional. If not set, we launch a new VM.
OLD_VM_IP=$3

# ID of the vm (based on the public ip).
VM_ID=$(echo $PUBLIC_VM_IP | tr . -)

# Function to generate host namespace virtual Ethernet IP
function gen_hostns_veth_ip {
    byte1=10
    byte2=$(($CLONE_ID / 30))
    byte3=$(($CLONE_ID % 30))
    byte3=$(($byte3 * 8))
    byte4=1
    echo "$byte1.$byte2.$byte3.$byte4"
}

# Function to generate VM namespace virtual Ethernet IP
function gen_vmns_veth_ip {
    byte1=10
    byte2=$(($CLONE_ID / 30))
    byte3=$(($CLONE_ID % 30))
    byte3=$(($byte3 * 8))
    byte4=2
    echo "$byte1.$byte2.$byte3.$byte4"
}

# Load new VM configuration
function load_new {
    # Kernel image used in the vm.
    sudo cp $DIR/hello-vmlinux.bin $VM_DIR/
    KERNEL=/hello-vmlinux.bin

    # Root filesystem to be used in the VM.
    sudo cp $DIR/hello-rootfs.ext4 $VM_DIR/
    ROOTFS=/hello-rootfs.ext4

    # VM memory and core config (memory in MB and number of vcores).
    VM_MEM=64
    VM_CPU=1

    # Kernel arguments (including network configuration).
    KERNEL_BOOT_ARGS="ro console=ttyS0 noapic reboot=k panic=1 pci=off nomodules random.trust_cpu=on ip=$VM_TAP_IP::$HOST_TAP_IP:$TAP_MASK_LONG::$VM_DEV:off"

    if [ ${ARCH} = "aarch64" ]; then
        KERNEL_BOOT_ARGS="keep_bootcon ${KERNEL_BOOT_ARGS}"
    fi

    echo "Booting VM with kernel image: $KERNEL, rootfs: $ROOTFS, mem: $VM_MEM, vcpu: $VM_CPU, boot_args: $KERNEL_BOOT_ARGS"

    # Configures kernel its arguments.
    sudo curl --unix-socket $VM_SOCKET \
        -X PUT "http://localhost/boot-source" \
        --data "{
        \"kernel_image_path\": \"${KERNEL}\",
        \"boot_args\": \"${KERNEL_BOOT_ARGS}\"
        }"
    
    echo "Kernel configured"
    echo "Configuring rootfs: $ROOTFS"

    # Configures the rootfs.
    sudo curl --unix-socket $VM_SOCKET -i \
        -X PUT "http://localhost/drives/rootfs" \
        -d "{
        \"drive_id\": \"rootfs\",
        \"path_on_host\": \"${ROOTFS}\",
        \"is_root_device\": true,
        \"is_read_only\": false
        }"

    echo "Rootfs configured"

    # Confiures resources.
    sudo curl --unix-socket $VM_SOCKET \
        -X PUT "http://localhost/machine-config" \
        --data "{
        \"vcpu_count\": ${VM_CPU},
        \"mem_size_mib\": ${VM_MEM},
        \"track_dirty_pages\": false
        }"

    echo "Resources configured"
    echo "Configuring network"

    # Confiures network.
    sudo curl --unix-socket $VM_SOCKET -i \
        -X PUT 'http://localhost/network-interfaces/eth0' \
        -d "{
        \"iface_id\": \"${VM_DEV}\",
        \"guest_mac\": \"${VM_MAC}\",
        \"host_dev_name\": \"${TAP}\"
        }"

    # API requests are handled asynchronously, it is important the configuration is
    # set, before `InstanceStart`.
    echo "Network configured"
    echo "Starting VM"
    sleep 5s

    # Launches vm.
    sudo curl --unix-socket $VM_SOCKET -i \
        -X PUT "http://localhost/actions" \
        -d "{
        \"action_type\": \"InstanceStart\"
        }"

    echo "VM started"
}

# Load VM from snapshot
function load_snapshot {
    # Snapshot file paths (files inside the chroot).
    VM_SNAP_FILE=snapshot_file
    VM_SNAP_MEM=mem_file

    # Copy snapshot (and disk) from the old VM directory.
    sudo cp -r $DIR/$OLD_VM_IP/root/hello-rootfs.ext4 $VM_DIR
    sudo cp -r $DIR/$OLD_VM_IP/root/hello-vmlinux.bin $VM_DIR
    sudo cp -r $DIR/$OLD_VM_IP/root/mem_file          $VM_DIR
    sudo cp -r $DIR/$OLD_VM_IP/root/snapshot_file     $VM_DIR

    sudo curl --unix-socket $VM_SOCKET -i \
        -X PUT "http://localhost/snapshot/load" \
        -d "{
        \"snapshot_path\": \"$VM_SNAP_FILE\",
        \"mem_file_path\": \"$VM_SNAP_MEM\",
        \"enable_diff_snapshots\": false,
        \"resume_vm\": true
        }"
}

# Internal (VM tap IP) and external (host tap IP) IPs and masks (same for all clones).
HOST_TAP_IP=192.168.241.1
VM_TAP_IP=192.168.241.2
TAP=vmtap
TAP_MASK_SHORT=29
TAP_MASK_LONG=255.255.255.248

# Veth IPs, mask, and names both in the host and in the VM namespaces.
HOST_NS_VETH_IP=$(gen_hostns_veth_ip)
HOST_NS_VETH=veth$HOST_NS_VETH_IP
VM_NS_VETH_IP=$(gen_vmns_veth_ip)
VM_NS_VETH=veth$VM_NS_VETH_IP
VETH_MASK_SHORT=24

# Default network device and MAC used in the VM.
VM_DEV=eth0
VM_MAC=$(printf 'DE:AD:BE:EF:%02X:%02X\n' $((RANDOM%256)) $((RANDOM%256)))

# Create VM tap in VM namespace.
echo "Creating tap device in namespace ns$CLONE_ID, called $TAP, with IP $VM_TAP_IP/$TAP_MASK_SHORT"
sudo ip netns exec ns$CLONE_ID ip tuntap add dev $TAP mode tap
sudo ip netns exec ns$CLONE_ID ip addr add $HOST_TAP_IP/$TAP_MASK_SHORT dev $TAP
sudo ip netns exec ns$CLONE_ID ip link set dev $TAP up

# Create VM veth pair.
echo "Creating veth pair in namespace ns$CLONE_ID, called $VM_NS_VETH and $HOST_NS_VETH, with IPs $VM_NS_VETH_IP/$VETH_MASK_SHORT and $HOST_NS_VETH_IP/$VETH_MASK_SHORT"
sudo ip netns exec ns$CLONE_ID ip link add $HOST_NS_VETH type veth peer name $VM_NS_VETH
sudo ip netns exec ns$CLONE_ID ip addr add $VM_NS_VETH_IP/$VETH_MASK_SHORT dev $VM_NS_VETH
sudo ip netns exec ns$CLONE_ID ip link set dev $VM_NS_VETH up

# Move one end to the host namespace.
echo "Moving $HOST_NS_VETH to host namespace"
sudo ip netns exec ns$CLONE_ID ip link set $HOST_NS_VETH netns 1
sudo ip addr add $HOST_NS_VETH_IP/$VETH_MASK_SHORT dev $HOST_NS_VETH
sudo ip link set dev $HOST_NS_VETH up

# Designate the outer end as default gateway for packets leaving the namespace.
sudo ip netns exec ns$CLONE_ID ip route add default via $HOST_NS_VETH_IP dev $VM_NS_VETH

# For packets that leave the namespace and have the source IP address of the original guest, rewrite the source address to public clone address.
sudo ip netns exec ns$CLONE_ID iptables -t nat -A POSTROUTING -o $VM_NS_VETH -s $VM_TAP_IP -j SNAT --to $PUBLIC_VM_IP

# Do the reverse operation; rewrites the destination address of packets heading towards the clone address to VM tap IP.
sudo ip netns exec ns$CLONE_ID iptables -t nat -A PREROUTING -i $VM_NS_VETH -d $PUBLIC_VM_IP -j DNAT --to $VM_TAP_IP

# Adds a route on the host for the clone address.
sudo ip route add $PUBLIC_VM_IP via $VM_NS_VETH_IP

echo "Connection flows: Host <-> $HOST_NS_VETH ($HOST_NS_VETH_IP) <-> ns$CLONE_ID <-> $VM_NS_VETH ($VM_NS_VETH_IP) <-> VM (TAP: $VM_TAP_IP)"
echo "In order to connect to the VM, use the IP $PUBLIC_VM_IP"

# Directory where the socket and logs of the VM will be placed (the link is created to avoid long paths).
VM_DIR=$DIR/$PUBLIC_VM_IP/root
rm $VM_DIR &> /dev/null
sudo ln -s $DIR/$PUBLIC_VM_IP/$FIRECRACKER_BINARY/$VM_ID/root $VM_DIR

# Socket that will be used to control the VM.
VM_SOCKET=$VM_DIR/firecracker.socket


if [ -z $OLD_VM_IP ]; then
    load_new
else
    load_snapshot
fi
