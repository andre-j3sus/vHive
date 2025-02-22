# Remote Firecracker Snapshots PoC

This guide explains how to create and boot firecracker-containerd VMs from snapshots with the Stargz containerd snapshotter. This proof-of-concept (PoC) demonstrates how to create snapshots and retrieve/boot them from a remote storage location.

[Stargz](https://github.com/containerd/stargz-snapshotter) is a containerd snapshotter that enables lazy pulling of container images. Instead of downloading an entire image, you can pull only the necessary layers to run a container. This functionality is powered by [eStargz](https://github.com/containerd/stargz-snapshotter/blob/main/docs/stargz-estargz.md), a lazily pullable image format introduced by the Stargz project.

### Table of Contents

- [Remote Firecracker Snapshots PoC](#remote-firecracker-snapshots-poc)
  - [Table of Contents](#table-of-contents)
  - [Setup](#setup)
    - [Setting Up a Registry](#setting-up-a-registry)
    - [Setting Up Stargz](#setting-up-stargz)
      - [eStargz Images](#estargz-images)
    - [Setting Up MinIO](#setting-up-minio)
  - [Usage](#usage)
    - [Booting a VM and Taking a Snapshot](#booting-a-vm-and-taking-a-snapshot)
    - [Booting a VM from a Snapshot](#booting-a-vm-from-a-snapshot)
  - [Program Workflow](#program-workflow)
    - [Booting a VM](#booting-a-vm)
    - [Taking a Snapshot](#taking-a-snapshot)
    - [Booting from a Snapshot](#booting-from-a-snapshot)

## Setup

Follow these steps to set up the environment for using remote firecracker-containerd snapshots with Stargz:

1. Clone the vHive repository and checkout to the `remote-snapshots-stargz` branch:

   ```bash
   git clone https://github.com/andre-j3sus/vHive.git vhive
   cd vhive
   git checkout remote-snapshots-stargz
   ```

2. Run the following command to install Go (default version: `1.23.3`). You can change the version by modifying `GoVersion` in `configs/setup/system.json`:

   ```bash
   ./scripts/install_go.sh; source /etc/profile
   ```

3. Install Docker by following the official [Docker installation guide for Ubuntu](https://docs.docker.com/engine/install/ubuntu/#install-using-the-repository). After installation, complete the [post-installation steps](https://docs.docker.com/engine/install/linux-postinstall/) to manage Docker as a non-root user.

4. Run the setup script to configure the environment. This assumes that you are using a CloudLab node:

   ```bash
   ./scripts/cloudlab/setup_node.sh
   ```

5. Navigate to the remote-firecracker-snapshots-poc directory, create a folder for storing snapshots, and build the Go program:

   ```bash
   cd remote-firecracker-snapshots-poc
   mkdir snaps
   go build
   ```

### Setting Up a Registry

This program retrieves container images from a registry. You can use a public registry such as Docker Hub or GitHub Container Registry, or set up your own using the [`registry:2`](https://hub.docker.com/_/registry) image.

To run a local registry, you need containerd and a container runtime like Docker or nerdctl. [nerdctl](https://github.com/containerd/nerdctl) is a CLI for containerd that provides a Docker-compatible interface, allowing you to use familiar Docker commands.

1. [Optional] Install nerdctl:

   ```bash
   sudo sh ./scripts/install_nerdctl.sh
   ```

2. Start the registry using either docker or nerdctl:

   ```bash
   docker run -d --network host --name registry registry:2
   # or
   sudo nerdctl run -d -p 5000:5000 --name registry registry:2
   ```

3. Check if the registry is running by making a request to its API:

   ```bash
   curl http://localhost:5000/v2/_catalog # Should return {"repositories":[]}
   ```

4. You can pull an image from Docker Hub and push it to your local registry.

   > **Note:** It is recommended to use nerdctl instead of Docker for this step because Docker may corrupt eStargz images, leading to errors when starting containers.

   ```bash
   sudo nerdctl pull docker.io/curiousgeorgiy/nginx:1.17-alpine-esgz
   sudo nerdctl tag docker.io/curiousgeorgiy/nginx:1.17-alpine-esgz localhost:5000/curiousgeorgiy/nginx:1.17-alpine-esgz
   sudo nerdctl push localhost:5000/curiousgeorgiy/nginx:1.17-alpine-esgz
   ```

### Setting Up Stargz

1. Build the rootfs with the stargz snapshotter. Follow the steps in [Getting started with remote snapshotters in firecracker-containerd](https://github.com/andre-j3sus/firecracker-containerd/blob/main/docs/remote-snapshotter-getting-started.md):

   1. Clone the [firecracker-containerd](https://github.com/andre-j3sus/firecracker-containerd) repository.

      > **Note:** A minor adjustment was made to the example code to allow pulling images from Docker Hub and insecure registries. The official example only permits images from GitHub Container Registry.

      ```bash
      git clone --recurse-submodules https://github.com/andre-j3sus/firecracker-containerd
      ```

   2. Build and install the stargz snapshotter image. To enable Stargz to pull images from your local registry, edit `firecracker-containerd/tools/image-builder/files_stargz/etc/containerd-stargz-grpc/config.toml`, and then run the following commands:

      ```bash
      make
      make image-stargz
      make install-stargz-rootfs
      ```

2. Configure demux-snapshotter:

   ```bash
   ./scripts/setup_demux_snapshotter.sh
   ```

#### eStargz Images

The stargz snapshotter uses a new image format called [eStargz](https://github.com/containerd/stargz-snapshotter/blob/main/docs/estargz.md).

You can try to use their [pre-converted images](https://github.com/containerd/stargz-snapshotter/blob/main/docs/pre-converted-images.md), or [build your own using the BuildKit](https://github.com/containerd/stargz-snapshotter/tree/main?tab=readme-ov-file#building-estargz-images-using-buildkit). For example:

```bash
docker buildx build --tag devandrejesus/fibonacci-python:esgz --target fibonacciPython -f .\Dockerfile -o type=registry,oci-mediatypes=true,compression=estargz,force-compression=true ..\..\
```

Alternatively, you can use [`ctr-remote`](https://github.com/containerd/stargz-snapshotter/tree/main?tab=readme-ov-file#creating-estargz-images-using-ctr-remote) to convert an existing image into eStargz while optimizing it for specific workloads.

Here are some images that have already been converted to eStargz:

- [fibonacci-python](https://hub.docker.com/r/devandrejesus/fibonacci-python)

### Setting Up MinIO

[MinIO](https://min.io/) is a high-performance object storage server that is API-compatible with Amazon S3. It can be used to store snapshots in a remote location.

1. Follow the [official guide](https://min.io/docs/minio/container/index.html) to run a MinIO server inside a Docker container:

   ```bash
   mkdir -p ${HOME}/minio/data

   docker run --network host \
    -e "MINIO_ROOT_USER=ROOTUSER" \
    -e "MINIO_ROOT_PASSWORD=CHANGEME123" \
    --name minio1 \
    quay.io/minio/minio server /data --console-address ":9001"
   ```

2. Install the MinIO Client (mc)

   ```bash
   wget https://dl.min.io/client/mc/release/linux-amd64/mc
   chmod +x mc
   sudo mv mc /usr/local/bin/mc
   ```

3. Set up an alias for your local MinIO instance:

   ```bash
   mc alias set myminio http://localhost:9000 ROOTUSER CHANGEME123
   ```

4. Create a bucket. This will create a bucket called `snapshots` in the MinIO server:

   ```bash
   mc mb myminio/snapshots
   ```

---

## Usage

Before using the system, [start all necessary daemons](https://github.com/andre-j3sus/firecracker-containerd/blob/main/docs/remote-snapshotter-getting-started.md#start-all-of-the-host-daemons) in separate terminals:

```bash
sudo firecracker-containerd --config /etc/firecracker-containerd/config.toml
```

```bash
sudo demux-snapshotter
```

```bash
sudo http-address-resolver
```

### Booting a VM and Taking a Snapshot

1. Run the `remote-firecracker-snapshots-poc` program with the `-make-snap` flag.

   ```bash
   # Usage:
   # sudo ./remote-firecracker-snapshots-poc -make-snap \
   #    -id "<VM ID>" -image "<URI>" -revision "<revision ID>" \
   #    -snapshots-base-path "<path/to/snapshots/folder>" -use-remote-storage \
   #    -minio-access-key "<access key>" -minio-secret-key "<secret key>" \
   #    -minio-endpoint "<minio endpoint>" -minio-bucket "<minio bucket>"

   sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc \
    -make-snap -id "vm1" \
    -image "hp172.utah.cloudlab.us:5000/curiousgeorgiy/nginx:1.17-alpine-esgz" \
    -revision "nginx-0" \
    -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps" \
    -use-remote-storage -minio-access-key "ROOTUSER" \
    -minio-secret-key "CHANGEME123" \
    -minio-endpoint "localhost:9000" \
    -minio-bucket "snapshots"
   ```

   This will:

   1. Start a Firecracker VM running a container inside of it, using the specified container image.
   2. Create a snapshot of the VM’s state and store it in the `snaps` folder (and in MinIO if `-use-remote-storage` is set).
   3. Resume the VM.

2. To confirm that the VM is running, check the Firecracker logs for its assigned IP address. Test the running VM by sending a request:

   ```bash
   curl http://<VM IP address>:<container port>
   ```

### Booting a VM from a Snapshot

A VM can be booted from a local or remote snapshot:

- Local Snapshot: The snapshot files are stored on the same machine.
- Remote Snapshot: The snapshot files are stored in MinIO or another remote location.

For this, you need to perform the setup steps on two different machines. The first machine will take the snapshot, and the second machine will boot the VM from the snapshot.

You can either use the `rsync` command to copy the snapshot files from one machine to another, or you can use MinIO to store the snapshots in a remote location.

1. [Optional] Sync Snapshot Files Between Machines:

   ```bash
   # Copy snapshot files from source machine to local machine
   # rsync -avz <username>@<remote IP>:<path/to/snapshots/folder> <path/to/local/folder>
   rsync -avz ajesus@hp090.utah.cloudlab.us:/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps/nginx-0/ /home/ajesus/Desktop/nginx-0/

   # Move snapshot files to the target machine
   rsync -avz /home/ajesus/Desktop/nginx-0/ ajesus@hp086.utah.cloudlab.us:/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps/nginx-0/
   ```

   To verify file integrity, run:

   ```bash
   sha256sum <file>
   ```

2. Run the `remote-firecracker-snapshots-poc` program with the `-boot-from-snap` flag:

   ```bash
   # Usage:
   # sudo ./remote-firecracker-snapshots-poc -boot-from-snap \
   #    -id "<VM ID>" -revision "<revision ID>" \
   #    -snapshots-base-path "<path/to/snapshots/folder>" -use-remote-storage \
   #    -minio-access-key "<access key>" -minio-secret-key "<secret key>" \
   #    -minio-endpoint "<minio endpoint>" -minio-bucket "<minio bucket>"

   sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc \
    -boot-from-snap -id "vm5" \
    -revision "nginx-0" \
    -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps" \
    -use-remote-storage -minio-access-key "ROOTUSER" \
    -minio-secret-key "CHANGEME123" \
    -minio-endpoint "hp086.utah.cloudlab.us:9000" \
    -minio-bucket "snapshots"
   ```

   This will restore the VM to the exact state it was in when the snapshot was taken.

---

## Program Workflow

### Booting a VM

1. The program starts by configuring the network for the VM, following
   the [Network for Clones](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/network-for-clones.md)
   guide from the Firecracker repository. This is the same process that vHive uses, and it uses the same Go module.
2. After the network is configured, the program creates a new VM using the `firecracker` API. The VM is created with the
   specified image and revision.
3. With the VM running, the program starts a container inside the VM using `firecracker-containerd` with the specified
   image.

With the VM running, you can send requests to the VM using curl.

### Taking a Snapshot

1. The program starts by pausing the VM using the `firecracker` API.
2. Using the same API, the program creates a snapshot of the VM's state, generating two files:
   1. **Guest memory file**: Contains the memory of the VM: `mem_file`
   2. **MicroVM state file**: Contains the state of the VM: `snap_file`
3. The VM is resumed.

After the snapshot is taken, the VM and container will be resumed, and the program will keep running.

In summary, this process generates two files inside the desired folder:

1. `mem_file`: Contains the memory of the VM.
2. `snap_file`: Contains the state of the VM.

### Booting from a Snapshot

1. Configure the network for the VM (same as when booting a fresh VM).
2. Check if the snapshot files are available in the specified folder. If not, download them from the remote storage (MinIO).
3. Send a create VM request to `firecracker-containerd`, specifying the VM config, the `mem_file`, the `snap_file`.

After this, the VM will be booted from the snapshot, and the container will be started with the same state as when the
snapshot was taken.
