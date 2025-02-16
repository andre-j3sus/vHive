# Remote Firecracker Snapshots PoC

This guide provides instructions on how to create and boot from snapshots using the remote-firecracker-snapshots-poc
program, with the Stargz containerd snapshotter.
This program is a proof of concept that demonstrates the creation and booting of snapshots stored in a remote location.

[Stargz](https://github.com/containerd/stargz-snapshotter) is a container image format that allows you to lazily pull
container images. This means that you can pull only the layers you need to run a container, instead of pulling the
entire image. This is useful when you have a large image and you only need a few layers to run a container.

### Table of Contents

- [Remote Firecracker Snapshots PoC](#remote-firecracker-snapshots-poc)
    - [Table of Contents](#table-of-contents)
  - [Setup](#setup)
    - [Setup Remote Registry](#setup-remote-registry)
    - [Setup Stargz](#setup-stargz)
    - [Setup Min IO](#setup-min-io)
  - [Usage](#usage)
    - [Boot a VM and take a snapshot](#boot-a-vm-and-take-a-snapshot)
    - [Boot from a snapshot across different machines](#boot-from-a-snapshot-across-different-machines)
  - [Program Workflow](#program-workflow)
    - [Boot a VM](#boot-a-vm)
    - [Take a snapshot](#take-a-snapshot)
    - [Boot from a snapshot](#boot-from-a-snapshot)

## Setup

1. Clone the vHive repository and checkout the `remote-snapshots-stargz` branch:

    ```bash
    git clone https://github.com/andre-j3sus/vHive.git vhive
    cd vhive
    git checkout remote-snapshots-stargz
    ```

2. Install go by running the following command (This will install version `1.23.3`. You can configure the version to
   install in `configs/setup/system.json` as `GoVersion`):

    ```bash
    ./scripts/install_go.sh; source /etc/profile
    ```

3. Install docker: https://docs.docker.com/engine/install/ubuntu/#install-using-the-repository and don't forget the
   post-installation steps: https://docs.docker.com/engine/install/linux-postinstall/

4. Setup the environment:

    ```bash
    ./scripts/cloudlab/setup_node.sh
    ```

5. Build the go program and create the folder to store the snapshots:

    ```bash
    cd remote-firecracker-snapshots-poc
    mkdir snaps
    go build
    ```

### Setup Remote Registry

This program uses a registry to store the container images created when taking snapshots. By default, the program uses a
local registry running on the same machine.

You can use the [`registry:2`](https://hub.docker.com/_/registry) image to run a registry.

For this, you need to have **containerd** and **nerdctl** installed on the node. Nerctl is a CLI for containerd that
provides a Docker-compatible command-line interface for containerd, which is nice because it allows you to use the same
commands you would use with Docker.

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
    2. If it's a local registry, you don't need to make any changes. If it's a remote registry, you need to update the
       URL from `localhost:5000` to `<remote IP>:5000`, e.g., `hp090.utah.cloudlab.us:5000`.

   Now you can pull an image from Docker Hub, and push it to the registry, for example:

    ```bash
    sudo nerdctl pull docker.io/curiousgeorgiy/nginx:1.17-alpine-esgz
    sudo nerdctl tag docker.io/curiousgeorgiy/nginx:1.17-alpine-esgz localhost:5000/curiousgeorgiy/nginx:1.17-alpine-esgz
    sudo nerdctl push localhost:5000/curiousgeorgiy/nginx:1.17-alpine-esgz
    ```

### Setup Stargz

1. Build the rootfs with the stargz snapshotter. Following
   the [Getting started with remote snapshotters in firecracker-containerd](https://github.com/andre-j3sus/firecracker-containerd/blob/main/docs/remote-snapshotter-getting-started.md)
   guide:

    1. Clone the firecracker-containerd repository. I had to make a small adjustment in the example code to allow us to
       pull
       images from Docker Hub, or an insecure registry (the official example only allows images from GitHub Container
       Registry):

       ```bash
       git clone --recurse-submodules https://github.com/andre-j3sus/firecracker-containerd
       ```

    2. Build and install the stargz snapshotter image. If you want to allow stargz to pull from your local registry (steps described [above](#setup-remote-registry)), edit `firecracker-containerd/tools/image-builder/files_stargz/etc/containerd-stargz-grpc/config.toml` before running:

        ```bash
        make
        make image-stargz
        make install-stargz-rootfs
       ```

2. Configure demux-snapshotter:

    ```bash
    ./scripts/setup_demux_snapshotter.sh
    ```

### Setup Min IO

[MinIO](https://min.io/) is a high-performance object storage server that is API-compatible with Amazon S3. You can use MinIO to store the snapshots in a remote location.

Follow this [guide](https://min.io/docs/minio/container/index.html) to start a MinIO server in a container using Docker:

```bash
mkdir -p ${HOME}/minio/data

docker run --network host -e "MINIO_ROOT_USER=ROOTUSER" -e "MINIO_ROOT_PASSWORD=CHANGEME123" --name minio1 quay.io/minio/minio server /data --console-address ":9001"
```
---

## Usage

Begin
by [starting all of the host-daemons](https://github.com/andre-j3sus/firecracker-containerd/blob/main/docs/remote-snapshotter-getting-started.md#start-all-of-the-host-daemons)
(each in a separate shell):

```bash
sudo firecracker-containerd --config /etc/firecracker-containerd/config.toml
```

```bash
sudo demux-snapshotter
```

```bash
sudo http-address-resolver
```

### Boot a VM and take a snapshot

1. Run the `remote-firecracker-snapshots-poc` program with the `-make-snap` flag.

    ```bash
    # Usage: sudo ./remote-firecracker-snapshots-poc -make-snap -id "<VM ID>" -image "<URI>" -revision "<revision ID>" -snapshots-base-path "<path/to/snapshots/folder>"
    sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc -make-snap -id "vm1" -image "hp172.utah.cloudlab.us:5000/curiousgeorgiy/nginx:1.17-alpine-esgz" -revision "nginx-0" -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps" -use-remote-storage -minio-access-key "ROOTUSER" -minio-secret-key "CHANGEME123" # Port 80
    ```

   This will start a VM with the specified image and create a snapshot of the VM's state. The snapshot will be stored in
   the `snaps` folder. After the snapshot is created, the VM will keep running. This is an image of nginx running on
   port `80`.

2. Now, the uVM is started and this is confirmed by the logs of firecracker-containerd, which also gives the IP address
   of the uVM. Send a request to the VM using curl:

    ```bash
    curl http://<VM IP address>:<container port>
    ```

### Boot from a snapshot across different machines

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

2. Run the `remote-firecracker-snapshots-poc` program with the `-boot-from-snap` flag:

    ```bash
    # sudo ./remote-firecracker-snapshots-poc -boot-from-snap -id "<VM ID>" -revision "<revision ID>" -snapshots-base-path "<path/to/snapshots/folder>"
    sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc -boot-from-snap -id "vm5" -revision "nginx-0" -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps" -use-remote-storage -minio-access-key "ROOTUSER" -minio-secret-key "CHANGEME123"
    ```

   This will boot a VM from the specified snapshot. The VM will be started with the same state as when the snapshot was
   taken. The VM will keep running after the snapshot is booted.

---

## Program Workflow

### Boot a VM

1. The program starts by configuring the network for the VM, following
   the [Network for Clones](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/network-for-clones.md)
   guide from the Firecracker repository. This is the same process that vHive uses, and it uses the same Go module.
2. After the network is configured, the program creates a new VM using the `firecracker` API. The VM is created with the
   specified image and revision.
3. With the VM running, the program starts a container inside the VM using `firecracker-containerd` with the specified
   image.

With the VM running, you can send requests to the VM using curl.

### Take a snapshot

1. The program starts by pausing the VM using the `firecracker` API.
2. Using the same API, the program creates a snapshot of the VM's state, generating two files:
    1. **Guest memory file**: Contains the memory of the VM: `mem_file`
    2. **MicroVM state file**: Contains the state of the VM: `snap_file`
3. After this, we
   use [nerdctl](https://github.com/containerd/nerdctl/blob/main/docs/command-reference.md#whale-nerdctl-commit) to
   **[commit](https://docs.docker.com/reference/cli/docker/container/commit/) the container** running inside the VM,
   creating a new image with the specified revision. This creates a new image with the container's changes, like
   creating a snapshot of the container. And then push this image to the registry.
    1. We create a third file, called `info_file` containing the container image name and the container snapshot commit
       name.
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
4. Send a create VM request to `firecracker-containerd`, specifying the VM config, the `mem_file`, the `snap_file`, and
   the container snapshot image.

After this, the VM will be booted from the snapshot, and the container will be started with the same state as when the
snapshot was taken.
