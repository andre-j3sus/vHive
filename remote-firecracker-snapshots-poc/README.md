# Remote Firecracker Snapshots PoC

This guide explains how to create and boot firecracker-containerd VMs from snapshots with the Stargz containerd snapshotter. This proof-of-concept (PoC) demonstrates how to create snapshots and retrieve/boot them from a remote storage location.

[Stargz](https://github.com/containerd/stargz-snapshotter) is a containerd snapshotter that enables lazy pulling of container images. Instead of downloading an entire image, you can pull only the necessary layers to run a container. This functionality is powered by [eStargz](https://github.com/containerd/stargz-snapshotter/blob/main/docs/stargz-estargz.md), a lazily pullable image format introduced by the Stargz project.

### Table of Contents

- [Remote Firecracker Snapshots PoC](#remote-firecracker-snapshots-poc)
  - [Table of Contents](#table-of-contents)
  - [How It Works](#how-it-works)
    - [Booting a VM](#booting-a-vm)
    - [Taking a Snapshot](#taking-a-snapshot)
    - [Booting from a Snapshot](#booting-from-a-snapshot)
  - [Setup](#setup)
    - [Setting Up a Registry](#setting-up-a-registry)
    - [Setting Up Stargz](#setting-up-stargz)
      - [eStargz Images](#estargz-images)
    - [Setting Up MinIO](#setting-up-minio)
  - [Usage](#usage)
    - [Booting a VM and Taking a Snapshot](#booting-a-vm-and-taking-a-snapshot)
    - [Booting a VM from a Snapshot](#booting-a-vm-from-a-snapshot)
  - [Limitations and Findings](#limitations-and-findings)
  - [To Do](#to-do)
  - [Future Work](#future-work)

## How It Works

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

   These files are stored in the specified folder.

3. `fallocate --dig-holes` is used to optimize the memory file by removing zeroed pages.
4. If the `-use-remote-storage` flag is set, the files are also stored in a remote storage, allowing them to be retrieved from another node. This works as follows:

   1. The program uploads the snapshot files to MinIO, a high-performance object storage server that is API-compatible with Amazon S3. To optimize storage, a **deduplication** mechanism is applied to the memory file, while the other files are uploaded as is. This deduplication process occurs in each node, and works as follows:
      1. The program chunks the memory file and calculates the SHA256 hash of each chunk.
      2. The program checks if the chunk already exists in MinIO by querying the Redis server. The goal of the Redis server is to store the hashes of the chunks that are already in MinIO to avoid uploading them again.
      3. If the chunk is not found in MinIO, the program uploads it to MinIO and stores the hash in the Redis server.

5. The VM is resumed.

### Booting from a Snapshot

1. Configure the network for the VM (same as when booting a fresh VM).
2. Check if the snapshot files are available in the specified folder. If not, download them from the remote storage (MinIO). This also involves reconstructing the memory file by downloading the chunks from MinIO and concatenating them.
3. Send a create VM request to `firecracker-containerd`, specifying the VM config, the `mem_file`, the `snap_file`.

After this, the VM will be booted from the snapshot, and the container will be started with the same state as when the
snapshot was taken.

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

6. Start a local registry, MinIO server, and a Redis server using docker compose:

   ```bash
   docker compose up -d
   ```

### Setting Up a Registry

This program retrieves container images from a registry. You can use a public registry such as Docker Hub or GitHub Container Registry, or set up your own using the [`registry:2`](https://hub.docker.com/_/registry) image.

To run a local registry, you need containerd and a container runtime like Docker or nerdctl. [nerdctl](https://github.com/containerd/nerdctl) is a CLI for containerd that provides a Docker-compatible interface, allowing you to use familiar Docker commands.

1. [Optional] Install nerdctl:

   ```bash
   sudo sh ./scripts/install_nerdctl.sh
   ```

2. Start the registry using either docker or nerdctl. **This is not needed if you started the registry using docker compose.**

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
# docker buildx build --tag <image-name>:<tag> -f <Dockerfile> -o type=registry,oci-mediatypes=true,compression=estargz,force-compression=true <context>
docker buildx build --tag devandrejesus/fibonacci-python:esgz -f .\Dockerfile -o type=registry,oci-mediatypes=true,compression=estargz,force-compression=true .
```

Alternatively, you can use [`ctr-remote`](https://github.com/containerd/stargz-snapshotter/tree/main?tab=readme-ov-file#creating-estargz-images-using-ctr-remote) to convert an existing image into eStargz while optimizing it for specific workloads.

Here are some images that have already been converted to eStargz:

- [nginx](https://hub.docker.com/r/curiousgeorgiy/nginx)
- [hello-world-python](https://hub.docker.com/r/devandrejesus/hello-world-python)
- [matrix-multiplier-python](https://hub.docker.com/r/devandrejesus/matrix-multiplier-python)
- [word-counter-go](https://hub.docker.com/r/devandrejesus/word-counter-go)

More images can be found in [this repository](https://github.com/andre-j3sus/faas-examples).

### Setting Up MinIO

**This is not needed if you started MinIO using docker compose.**

[MinIO](https://min.io/) is a high-performance object storage server that is API-compatible with Amazon S3. It can be used to store snapshots in a remote location.

1. Follow the [official guide](https://min.io/docs/minio/container/index.html) to run a MinIO server inside a Docker container.

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

4. [Optional] Create a bucket. This will create a bucket called `snapshots` in the MinIO server:

   ```bash
   mc mb myminio/snapshots
   ```

   This is optional because the program will create the bucket if it does not exist.

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
   #    -minio-endpoint "<minio endpoint>" -minio-bucket "<minio bucket>" \^
   #    -redis-addr "<redis address>"

   sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc \
    -make-snap -id "vm1" \
    -image "hp172.utah.cloudlab.us:5000/curiousgeorgiy/nginx:1.17-alpine-esgz" \
    -revision "nginx-0" \
    -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps" \
    -use-remote-storage -minio-access-key "ROOTUSER" \
    -minio-secret-key "CHANGEME123" \
    -minio-endpoint "localhost:9000" \
    -minio-bucket "snapshots" \
    -redis-addr "localhost:6379"
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
   #    -minio-endpoint "<minio endpoint>" -minio-bucket "<minio bucket>" \
   #    -redis-addr "<redis address>"

   sudo ./remote-firecracker-snapshots-poc/remote-firecracker-snapshots-poc \
    -boot-from-snap -id "vm5" \
    -revision "nginx-0" \
    -snapshots-base-path "/users/ajesus/vhive/remote-firecracker-snapshots-poc/snaps" \
    -use-remote-storage -minio-access-key "ROOTUSER" \
    -minio-secret-key "CHANGEME123" \
    -minio-endpoint "hp086.utah.cloudlab.us:9000" \
    -minio-bucket "snapshots" \
    -redis-addr "hp086.utah.cloudlab.us:6379"
   ```

   This will restore the VM to the exact state it was in when the snapshot was taken.

---

## Limitations and Findings

During the snapshot restoration process, four MMIO devices are required to restore the VM to its previous state. These devices are:

1. **ConnectedBlockState**: I'm not sure what this device is used for. It is connected to the `ctrstub0` file, located at `/var/lib/firecracker-containerd/shim-base/vmX#vmX/ctrstub0`, where `vmX` is the VM ID.
2. **ConnectedBlockState**: This device is used to restore the root drive of the VM. The device is connected to the `rootfs-stargz.img` file.
3. **ConnectedNetState**: This device is used to restore the network configuration of the VM. The device is connected to the `tap0` interface.
4. **ConnectedVsockState**: This device is used to restore the vsock configuration of the VM. The device is connected to the `firecracker.vsock` file.

Here's a snippet of the logs showing the restoration of the MMIO devices (these logs are not native to Firecracker, but were added by me to debug the snapshot restoration process):

```bash
DEBU[2025-02-20T15:55:26.157311107-07:00] Restoring MMIO devices... jailer=noop runtime=aws.firecracker vmID=vm7 vmm_stream=stdout
DEBU[2025-02-20T15:55:26.157367624-07:00] Device 0: ConnectedBlockState { device_id: "MN2HE43UOVRDA", device_state: BlockState { id: "MN2HE43UOVRDA", partuuid: None, cache_type: Unsafe, root_device: false, disk_path: "/var/lib/firecracker-containerd/shim-base/vm1#vm1/ctrstub0", virtio_state: VirtioDeviceState { device_type: 2, avail_features: 4831838208, acked_features: 4831838208, queues: [QueueState { max_size: 256, size: 256, ready: true, desc_table: 60833792, avail_ring: 60837888, used_ring: 60841984, next_avail: 5, next_used: 5, num_added: 0 }], interrupt_status: 0, activated: true }, rate_limiter_state: RateLimiterState { ops: None, bandwidth: None }, file_engine_type: Sync }, transport_state: MmioTransportState { features_select: 0, acked_features_select: 0, queue_select: 0, device_status: 15, config_generation: 0 }, mmio_slot: MMIODeviceInfo { addr: 3489665024, len: 4096, irqs: [6] } } jailer=noop runtime=aws.firecracker vmID=vm7 vmm_stream=stdout
DEBU[2025-02-20T15:55:26.157821604-07:00] Device 1: ConnectedBlockState { device_id: "root_drive", device_state: BlockState { id: "root_drive", partuuid: None, cache_type: Unsafe, root_device: true, disk_path: "/var/lib/firecracker-containerd/runtime/rootfs-stargz.img", virtio_state: VirtioDeviceState { device_type: 2, avail_features: 4831838240, acked_features: 4831838240, queues: [QueueState { max_size: 256, size: 256, ready: true, desc_table: 63438848, avail_ring: 63442944, used_ring: 63447040, next_avail: 11404, next_used: 11404, num_added: 0 }], interrupt_status: 0, activated: true }, rate_limiter_state: RateLimiterState { ops: None, bandwidth: None }, file_engine_type: Sync }, transport_state: MmioTransportState { features_select: 0, acked_features_select: 0, queue_select: 0, device_status: 15, config_generation: 0 }, mmio_slot: MMIODeviceInfo { addr: 3489660928, len: 4096, irqs: [5] } } jailer=noop runtime=aws.firecracker vmID=vm7 vmm_stream=stdout
DEBU[2025-02-20T15:55:26.158258785-07:00] Device 2: ConnectedNetState { device_id: "1", device_state: NetState { id: "1", tap_if_name: "tap0", rx_rate_limiter_state: RateLimiterState { ops: None, bandwidth: None }, tx_rate_limiter_state: RateLimiterState { ops: None, bandwidth: None }, mmds_ns: Some(MmdsNetworkStackState { mac_addr: [6, 1, 35, 69, 103, 1], ipv4_addr: 2852039166, tcp_port: 80, max_connections: 30, max_pending_resets: 100 }), config_space: NetConfigSpaceState { guest_mac: [170, 252, 0, 0, 0, 1] }, virtio_state: VirtioDeviceState { device_type: 1, avail_features: 4294986915, acked_features: 4294986915, queues: [QueueState { max_size: 256, size: 256, ready: true, desc_table: 62783488, avail_ring: 62787584, used_ring: 62791680, next_avail: 184, next_used: 184, num_added: 184 }, QueueState { max_size: 256, size: 256, ready: true, desc_table: 62799872, avail_ring: 62803968, used_ring: 62808064, next_avail: 144, next_used: 144, num_added: 144 }], interrupt_status: 0, activated: true } }, transport_state: MmioTransportState { features_select: 0, acked_features_select: 0, queue_select: 1, device_status: 15, config_generation: 0 }, mmio_slot: MMIODeviceInfo { addr: 3489669120, len: 4096, irqs: [7] } } jailer=noop runtime=aws.firecracker vmID=vm7 vmm_stream=stdout
DEBU[2025-02-20T15:55:26.158327191-07:00] Device 3: ConnectedVsockState { device_id: "vsock", device_state: VsockState { backend: Uds(VsockUdsState { path: "firecracker.vsock" }), frontend: VsockFrontendState { cid: 0, virtio_state: VirtioDeviceState { device_type: 19, avail_features: 38654705664, acked_features: 4294967296, queues: [QueueState { max_size: 256, size: 256, ready: true, desc_table: 62062592, avail_ring: 62066688, used_ring: 62070784, next_avail: 53, next_used: 53, num_added: 53 }, QueueState { max_size: 256, size: 256, ready: true, desc_table: 62078976, avail_ring: 62083072, used_ring: 62087168, next_avail: 51, next_used: 51, num_added: 51 }, QueueState { max_size: 256, size: 256, ready: true, desc_table: 60850176, avail_ring: 60854272, used_ring: 60858368, next_avail: 0, next_used: 0, num_added: 0 }], interrupt_status: 0, activated: true } } }, transport_state: MmioTransportState { features_select: 0, acked_features_select: 0, queue_select: 2, device_status: 15, config_generation: 0 }, mmio_slot: MMIODeviceInfo { addr: 3489673216, len: 4096, irqs: [8] } } jailer=noop runtime=aws.firecracker vmID=vm7 vmm_stream=stdout
DEBU[2025-02-20T15:55:26.163580856-07:00] snapshot loaded successfully runtime=aws.firecracker
```

The current limitation is that I'm not sure what the `ConnectedBlockState` device is used for. The file only contains a single string which is the device ID (e.g. `MN2HE43UOVRDA`). Also, to restore a remote snapshot, you need to move this file to the target machine manually.

---

## To Do

- [ ] Test PoC with other images. Use the vHive [examples](../function-images/).
- [ ] Test compression mechanisms for the memory file.
- [ ] Investigate what is the MMIO device needed to restore the VM from a snapshot. More information in [Current Limitations](#current-limitations).
- [ ] Investigate if there are better deduplication mechanisms for the memory file. Currently we use a simple chunk and hash approach.
- [ ] Fix bug with demux-snapshotter: when the VM is stopped, the demux-snapshotter crashes.
- [ ] Try to run the PoC in Ubuntu 22.04: I tried, but I got a network error with the vsock device. I need to investigate this further.

---

## Future Work

There are several optimizations that can be implemented to improve the performance of the system:

- **Pre-fetching of snapshots**. This would require a mechanism to predict the function invocation patterns and pre-fetch the snapshots that are likely to be used. Serverless in the Wild and ORION use some prediction mechanisms that could be adapted to this system.
- **Redirect requests to nodes with the snapshots**. This could be done by using a load balancer that is aware of the snapshots' locations.
- **Lazy loading of snapshots**. Instead of fetching the entire snapshot, only load the necessary parts of the snapshot. I'm not sure if this is possible to integrate with Firecracker, but it could be an interesting research direction.
