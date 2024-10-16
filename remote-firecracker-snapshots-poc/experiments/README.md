# Firecracker Network Namespace Tutorial

This tutorial will guide you through the process of setting up a Firecracker microVM on your local machine using network namespaces. Network namespaces are a Linux kernel feature that allows you to create multiple isolated network stacks on a single host. They are useful in this context because they allow you to create a network namespace for the Firecracker microVM, isolating it from the host network stack.

> Note: this tutorial assumes you are using a Linux machine.

1. Setup the environment:

    ```bash
    ./setup-enviroment.sh
    ```

    This script:
    - downloads the Firecracker (v1.9.1) binary if it is not already installed on your machine;
    - downloads a Linux kernel image (hello-vmlinux.bin) and a root filesystem image (hello-rootfs.ext4).

2. Start the Firecracker microVM:

    ```bash
    ./start-vm.sh <clone ID> <IP address>
    ```

    Replace `<IP address>` with the IP address you want to assign to the microVM, and `<clone ID>` with a unique identifier for the network namespace.
    - Uses **jailer** (a tool that sets up the Firecracker environment) to create a new network namespace and run the Firecracker binary inside it.

3. Configure the microVM. **Open a new terminal** and run:

    ```bash
    ./config-vm.sh <clone ID> <IP address>
    ```

    This script:
    - configures internal/external IP addresses and network configuration for `vm tap` and `veth` interfaces;
    - create a tap for vm ns communication;
    - create a veth pair for communication between vm ns and host ns;
    - set up the routing table for the vm ns;
    - set up the iptables for the vm ns
    - setups the VM, creating the necessary directories and files.

4. In the terminal where we started the microVM, log in with the following credentials:

    - Username: `root`
    - Password: `root`

    There you have it! You are now inside the Firecracker microVM.

5. Take a snapshot of the microVM:

    ```bash
    ./snapshot-vm.sh  <clone ID> <IP address>
    ```

    This script uses the Firecracker API to:
    - change the microVM state to `Paused`;
    - create a snapshot of the microVM, generating `snapshot-file`(a snapshot file that contains the microVM state) and a `mem-file` (a memory file that contains the microVM memory).
    - change the microVM state back to `Resumed`.

6. Stop the microVM:

    ```bash
    ./stop-vm.sh  <clone ID> <IP address>
    ```

    This script stops the Firecracker binary and deletes the microVM socket file and the TAP interface.

7. Restore the microVM from the snapshot. First, start the Firecracker microVM:

    ```bash
    ./start-vm.sh  <clone ID> <IP address>
    ```

    Then, restore the microVM from the snapshot:

    ```bash
    ./restore-vm.sh  <clone ID> <IP address>
    ```

    Or, configure other VM with the snapshot:

    ```bash
    ./config-vm.sh <clone ID> <IP address> <IP address of snapshotted VM>
    ```
