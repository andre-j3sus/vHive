# Remote Firecracker Snapshots PoC

This guide provides instructions on how to create and boot from snapshots using the remote-firecracker-snapshots-poc program. This program is a proof of concept that demonstrates the creation and booting of snapshots stored in a remote location.

## Setup

1. Clone the vHive repository and checkout the `remote-firecracker-snapshots-poc` branch:

```bash
git clone https://github.com/andre-j3sus/vHive.git
cd vHive
git checkout remote-firecracker-snapshots-poc
```

2. Install go, if you haven't already:

```bash
./scripts/install_go.sh; source /etc/profile
```

3. Setup the environment:

```bash
./scripts/cloudlab/setup_node.sh
```

4. Create the devmapper device:

```bash
./scripts/create_devmapper.sh
```

5. Build the go program and create the folder to store the snapshots:

```bash
pushd remote-firecracker-snapshots-poc
mkdir snaps
go mod tidy
go build
popd
```

6. Start firecracker-containerd in a new terminal:

```bash
sudo /usr/local/bin/firecracker-containerd --config /etc/firecracker-containerd/config.toml
```

---

## Usage

### Boot a VM and take a snapshot

1. Run the `remote-firecracker-snapshots-poc` program with the `-make-snap` flag:

```bash
# sudo ./remote-firecracker-snapshots-poc -make-snap -id "<VM ID>" -image "<URI>" -revision "<revision ID>" -snapshots-base-path "<path/to/snapshots/folder>"
sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc -make-snap -id "0" -image "docker.io/library/nginx:1.17-alpine" -revision "nginx-0" -snapshots-base-path "/users/ajesus/vHive/remote-firecracker-snapshots-poc/snaps" # Port 80
```

This will start a VM with the specified image and create a snapshot of the VM's state. The snapshot will be stored in the `snaps` folder. After the snapshot is created, the VM will keep running. This is an image of nginx running on port 80.

2. Now, the uVM is started and this is confirmed by the logs of firecracker-containerd, which also gives the IP address of the uVM. Send a request to the VM using curl:

```bash
curl http://<VM IP address>:<container port>
```

### Boot from a snapshot

6. Run the `remote-firecracker-snapshots-poc` program with the `-boot-from-snap` flag:

```bash
# sudo ./remote-firecracker-snapshots-poc -boot-from-snap -id "<VM ID>" -revision "<revision ID>" -snapshots-base-path "<path/to/snapshots/folder>"
sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc -boot-from-snap -id "1" -revision "nginx-0" -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps"
```

This will boot a VM from the specified snapshot. The VM will be started with the same state as when the snapshot was taken. The VM will keep running after the snapshot is booted.

---
---

## Other useful commands/info

- To check the tap interface status:

```bash
sudo ip link show tap0
```

- To check the IP address assigned to the tap interface:

```bash
sudo ip addr show tap0
```

- To check the routes:

```bash
sudo ip route show
```

- To check the network namespaces:

```bash
sudo ip netns list
```

- To set tap interface up:

```bash
sudo ip link set <tap interface> up
```

- To assign IP address to tap interface:

```bash
sudo ip addr add <IP address>/<subnet> dev <tap interface>
```

eg: `sudo ip addr add 172.18.0.1/24 dev tap0`

- To delete IP address from tap interface:

```bash
sudo ip addr del <IP address>/<subnet> dev <tap interface>
```


- The directory with VM socket, logs and file system is located at:

```bash
sudo ls -ls /var/lib/firecracker-containerd/shim-base/remote-firecracker-snapshots-poc/<VM ID>
```

- To check the logs of the uVM in real-time:

```bash
sudo tail -f /var/lib/firecracker-containerd/shim-base/remote-firecracker-snapshots-poc/<VM ID>/fc-logs.fifo
```

- To interact with VM firecracker API:

```bash
sudo curl --unix-socket /var/lib/firecracker-containerd/shim-base/remote-firecracker-snapshots-poc/<VM ID>/firecracker.sock http://localhost/machine-config
```

---

## Current issues

- The VM is not reachable from the host machine. I do not know if it's an issue with firecracker-containerd or the program itself. When I try to ping/curl the VM from the host machine, I get a "Destination Host Unreachable" error. When I check the VM logs, it seems that the VM is having trouble connecting with the tap interface.