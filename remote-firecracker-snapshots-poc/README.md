# Remote Firecracker Snapshots PoC

This guide provides instructions on how to create and boot from snapshots using the remote-firecracker-snapshots-poc program. This program is a proof of concept that demonstrates the creation and booting of snapshots stored in a remote location.

### Table of Contents

- [Remote Firecracker Snapshots PoC](#remote-firecracker-snapshots-poc)
    - [Table of Contents](#table-of-contents)
  - [Setup](#setup)
    - [Setup Remote Registry](#setup-remote-registry)
  - [Usage](#usage)
    - [Boot a VM and take a snapshot](#boot-a-vm-and-take-a-snapshot)
    - [Boot from a snapshot](#boot-from-a-snapshot)
  - [Program Workflow](#program-workflow)
    - [Boot a VM](#boot-a-vm)
    - [Take a snapshot](#take-a-snapshot)
    - [Boot from a snapshot](#boot-from-a-snapshot-1)
  - [Current blockers](#current-blockers)
    - [Hypothesis](#hypothesis)
  - [Using stargz snapshotter](#using-stargz-snapshotter)

## Setup

1. Clone the vHive repository and checkout the `remote-snapshots` branch:

```bash
git clone https://github.com/andre-j3sus/vHive.git vhive
cd vhive
git checkout remote-snapshots
```

2. Install go by running the following command (This will install version `1.21.1`. You can configure the version to install in `configs/setup/system.json` as `GoVersion`):
    
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
go build
popd
```

6. Start firecracker-containerd in a new terminal:

```bash
sudo /usr/local/bin/firecracker-containerd --config /etc/firecracker-containerd/config.toml
```

### Setup Remote Registry

This program uses a registry to store the container images created when taking snapshots. By default, the program uses a local registry running on the same machine.

You can use the [`registry:2`](https://hub.docker.com/_/registry) image to run a registry.

For this, you need to have **containerd** and **nerdctl** installed on the node. Nerctl is a CLI for containerd that provides a Docker-compatible command-line interface for containerd, which is nice because it allows you to use the same commands you would use with Docker.

1. Install nerdctl: https://gist.github.com/Faheetah/4baf1e413691bc4e7784fad16d6275a9 or execute the following commands:

```bash
sudo sh ./scripts/install_nerdctl.sh
```

2. Start containerd and run the registry:

```bash
sudo containerd &
sudo nerdctl run -d -p 5000:5000 --name registry registry:2
```

3. Test the registry by curling the registry's URL:

```bash
curl http://localhost:5000/v2/_catalog # Should return {"repositories":[]}
```

4. Update the `remote-firecracker-snapshots-poc` program to use the remote registry:
   1. Update the `commitCtrSnap` and `pullCtrSnapCommit` methods in `orchestrator.go` to use the remote registry.
   2. If it's a local registry, you don't need to make any changes. If it's a remote registry, you need to update the URL from `localhost:5000` to `<remote IP>:5000`, e.g., `hp090.utah.cloudlab.us:5000`.

---

## Usage

### Boot a VM and take a snapshot

1. Run the `remote-firecracker-snapshots-poc` program with the `-make-snap` flag.

```bash
# Usage: sudo ./remote-firecracker-snapshots-poc -make-snap -id "<VM ID>" -image "<URI>" -revision "<revision ID>" -snapshots-base-path "<path/to/snapshots/folder>"
sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc -make-snap -id "0" -image "docker.io/library/nginx:1.17-alpine" -revision "nginx-0" -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps" # Port 80
```

This will start a VM with the specified image and create a snapshot of the VM's state. The snapshot will be stored in the `snaps` folder. After the snapshot is created, the VM will keep running. This is an image of nginx running on port 80.

2. Now, the uVM is started and this is confirmed by the logs of firecracker-containerd, which also gives the IP address of the uVM. Send a request to the VM using curl:

```bash
curl http://<VM IP address>:<container port>
```

### Boot from a snapshot

1. Copy the snapshot files from the remote location to the local machine:

```bash
#rsync -avz <username>@<remote IP>:<path/to/snapshots/folder> <path/to/local/folder>
rsync -avz ajesus@hp090.utah.cloudlab.us:/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps/nginx-0/ /home/ajesus/Desktop/nginx-0/
```

and then, move them to the other remote machine:

```bash
rsync -avz /home/ajesus/Desktop/nginx-0/ ajesus@hp086.utah.cloudlab.us:/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps/nginx-0/
```

If you want, you can use `sha256sum` to check if the files are the same on both machines: `sha256sum <file>`.

1. Run the `remote-firecracker-snapshots-poc` program with the `-boot-from-snap` flag:

```bash
# sudo ./remote-firecracker-snapshots-poc -boot-from-snap -id "<VM ID>" -revision "<revision ID>" -snapshots-base-path "<path/to/snapshots/folder>"
sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc -boot-from-snap -id "1" -revision "nginx-0" -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps"
```

This will boot a VM from the specified snapshot. The VM will be started with the same state as when the snapshot was taken. The VM will keep running after the snapshot is booted.

---

## Program Workflow

### Boot a VM

1. The program starts by configuring the network for the VM, following the [Network for Clones](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/network-for-clones.md) guide from the Firecracker repository. This is the same process that vHive uses, and it uses the same Go module.
2. After the network is configured, the program creates a new VM using the `firecracker` API. The VM is created with the specified image and revision.
3. With the VM running, the program starts a container inside the VM using `firecracker-containerd` with the specified image.

With the VM running, you can send requests to the VM using curl.

### Take a snapshot

1. The program starts by pausing the VM using the `firecracker` API.
2. Using the same API, the program creates a snapshot of the VM's state, generating two files:
   1. **Guest memory file**: Contains the memory of the VM: `mem_file`
   2. **MicroVM state file**: Contains the state of the VM: `snap_file`
3. After this, we use [nerdctl](https://github.com/containerd/nerdctl/blob/main/docs/command-reference.md#whale-nerdctl-commit) to **[commit](https://docs.docker.com/reference/cli/docker/container/commit/) the container** running inside the VM, creating a new image with the specified revision. This creates a new image with the container's changes, like creating a snapshot of the container. And then push this image to the registry.
   1. We create a third file, called `info_file` containing the container image name and the container snapshot commit name.
4. The VM is resumed.

After the snapshot is taken, the VM and container will be resumed, and the program will keep running.

In summary, this process generates three files inside the desired folder:
1. `mem_file`: Contains the memory of the VM.
2. `snap_file`: Contains the state of the VM.
3. `info_file`: Contains the container image name and the container snapshot commit name.

### Boot from a snapshot

1. Configure the network for the VM (same as when booting a fresh VM).
2. Deserealize the `info_file` to get the container image name and the container snapshot commit name.
3. Pull the container image from the registry using the container image name.
4. Send a create VM request to `firecracker-containerd`, specifying the VM config, the `mem_file`, the `snap_file`, and the container snapshot image.

After this, the VM will be booted from the snapshot, and the container will be started with the same state as when the snapshot was taken.

---

## Current blockers

I'm facing the same blocker described [here](https://github.com/vhive-serverless/vHive/blob/main/docs/snapshots.md#blockers). Booting from a snapshot in the same node it was taken works, but booting from a snapshot in a different node doesn't work. The VM boots and the container starts, but the container disk gets corrupted once a request arrives. Before a request arrives, the container is running fine.

### Hypothesis

The image created with `nerdctl commit` is not the problem. I tried to run a container using the image, and it worked fine, even when pulled from the remote registry. So the problem seems to be either with firecracker-containerd or with the snapshotter we are using (devmapper).

I also tried to use the image to start a container in a fresh microVM, and it worked fine as well.

---

## Using stargz snapshotter 

I tried to follow the [Getting started with remote snapshotters in firecracker-containerd](https://github.com/firecracker-microvm/firecracker-containerd/blob/main/docs/remote-snapshotter-getting-started.md) guide.

1. Clone the firecracker-containerd repository:

```bash
git clone --recurse-submodules https://github.com/firecracker-microvm/firecracker-containerd
```

> **Note**: Steps 2 and 3 need to be executed in the firecracker-containerd repository. The rest of the steps should be executed in the vHive repository.

2. [Build a Linux kernel with FUSE support](https://github.com/firecracker-microvm/firecracker-containerd/blob/main/docs/remote-snapshotter-getting-started.md#build-a-linux-kernel-with-fuse-support):

```bash
KERNEL_VERSION=5.10 make kernel
KERNEL_VERSION=5.10 make install-kernel
```

3. [Build a Firecracker rootfs with a remote snapshotter](https://github.com/firecracker-microvm/firecracker-containerd/blob/main/docs/remote-snapshotter-getting-started.md#build-a-firecracker-rootfs-with-a-remote-snapshotter):
  
```bash
make image-stargz
make install-stargz-rootfs
```

4. Configure demux-snapshotter:

```bash
./scripts/setup_demux_snapshotter.sh
```

5. [Start all of the host-daemons](https://github.com/firecracker-microvm/firecracker-containerd/blob/main/docs/remote-snapshotter-getting-started.md#start-all-of-the-host-daemons):

Each in a separate shell:

```bash
sudo firecracker-containerd --config /etc/firecracker-containerd/config.toml
```

The following commands should be executed in the firecracker-containerd repository:

```bash
sudo snapshotter/demux-snapshotter
```

```bash
sudo snapshotter/http-address-resolver
```

6. Start a VM with the stargz snapshotter:

```bash
sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc -id "0" -image "ghcr.io/firecracker-microvm/firecracker-containerd/amazonlinux:latest-esgz" -revision "amazonlinux" -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps"
```