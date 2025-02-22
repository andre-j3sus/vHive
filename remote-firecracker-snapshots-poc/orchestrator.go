package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"syscall"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/containerd/remotes/docker/config"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/stargz-snapshotter/fs/source"
	fcclient "github.com/firecracker-microvm/firecracker-containerd/firecracker-control/client"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	"github.com/pkg/errors"
	"github.com/vhive-serverless/remote-firecracker-snapshots-poc/snapshotting"
	"github.com/vhive-serverless/vhive/networking"
	"log"
	"strings"
)

type VMInfo struct {
	imgName            string
	ctrSnapKey         string
	ctrSnapCommitName  string
	snapBooted         bool
	containerSnapMount *mount.Mount
	ctr                containerd.Container
	task               containerd.Task
}

type Orchestrator struct {
	cachedImages map[string]containerd.Image
	vms          map[string]VMInfo

	snapshotter     string
	client          *containerd.Client
	fcClient        *fcclient.Client
	snapshotService snapshots.Snapshotter
	leaseManager    leases.Manager
	leases          map[string]*leases.Lease
	networkManager  *networking.NetworkManager
	snapshotManager *snapshotting.SnapshotManager

	useRemoteStorage bool

	// Namespace for requests to containerd  API. Allows multiple consumers to use the same containerd without
	// conflicting eachother. Benefit of sharing content but still having separation with containers and images
	ctx context.Context
}

// NewOrchestrator Initializes a new orchestrator
func NewOrchestrator(snapshotter, containerdNamespace, snapsBaseFolder, minioEndpoint, accessKey, secretKey, bucket string, useRemoteStorage bool) (*Orchestrator, error) {
	var err error

	orch := new(Orchestrator)
	orch.cachedImages = make(map[string]containerd.Image)
	orch.vms = make(map[string]VMInfo)
	orch.snapshotter = snapshotter
	orch.ctx = namespaces.WithNamespace(context.Background(), containerdNamespace)
	orch.networkManager, err = networking.NewNetworkManager("", 10)
	if err != nil {
		return nil, errors.Wrapf(err, "creating network manager")
	}

	orch.useRemoteStorage = useRemoteStorage
	orch.snapshotManager, err = snapshotting.NewSnapshotManager(snapsBaseFolder, minioEndpoint, accessKey, secretKey, bucket, useRemoteStorage)
	if err != nil {
		return nil, errors.Wrapf(err, "creating snapshot manager")
	}

	// Connect to firecracker client
	log.Println("Creating firecracker client")
	orch.fcClient, err = fcclient.New(containerdTTRPCAddress)
	if err != nil {
		return nil, errors.Wrapf(err, "creating firecracker client")
	}
	log.Println("Created firecracker client")

	// Connect to containerd client
	log.Println("Creating containerd client")
	orch.client, err = containerd.New(containerdAddress)
	if err != nil {
		return nil, errors.Wrapf(err, "creating containerd client")
	}
	log.Println("Created containerd client")

	// Create containerd snapshot service
	orch.snapshotService = orch.client.SnapshotService(snapshotter)

	orch.leaseManager = orch.client.LeasesService()
	orch.leases = make(map[string]*leases.Lease)

	return orch, nil
}

// Converts an image name to a url if it is not a URL
func getImageURL(image string) string {
	// Pull from dockerhub by default if not specified (default k8s behavior)
	if strings.Contains(image, ".") {
		return image
	}

	// Handle local registry (e.g., localhost:5000)
	if strings.HasPrefix(image, "localhost:") {
		return image
	}

	return "docker.io/" + image
}

func (orch *Orchestrator) getContainerImage(vmID, imageName, dockerMetadataTemplate, dockerHost, dockerUser, dockerPass string) (*containerd.Image, error) {
	log.Println("Setting docker credential metadata")
	_, err := orch.fcClient.SetVMMetadata(orch.ctx, &proto.SetVMMetadataRequest{
		VMID:     vmID,
		Metadata: fmt.Sprintf(dockerMetadataTemplate, dockerHost, dockerUser, dockerPass),
	})
	if err != nil {
		return nil, fmt.Errorf("setting docker credential metadata: %w", err)
	}

	image, found := orch.cachedImages[imageName]
	if !found {
		var err error
		log.Printf("Pulling image %s\n", imageName)

		imageURL := getImageURL(imageName)
		options := docker.ResolverOptions{
			Hosts:  config.ConfigureHosts(orch.ctx, config.HostOptions{DefaultScheme: "http"}),
			Client: http.DefaultClient,
		}
		image, err = orch.client.Pull(orch.ctx, imageURL,
			containerd.WithPullUnpack,
			containerd.WithPullSnapshotter(snapshotter),
			containerd.WithResolver(docker.NewResolver(options)),
			// stargz labels to tell the snapshotter to lazily load the image
			containerd.WithImageHandlerWrapper(source.AppendDefaultLabelsHandlerWrapper(imageURL, 10*1024*1024)))

		if err != nil {
			return nil, errors.Wrapf(err, "pulling image")
		}
		log.Printf("Successfully pulled %s image with %s\n", image.Name(), snapshotter)

		orch.cachedImages[imageName] = image
	}

	return &image, nil
}

func (orch *Orchestrator) createVM(vmID string) error {
	createVMRequest := &proto.CreateVMRequest{
		VMID: vmID,
		// Enabling Go Race Detector makes in-microVM binaries heavy in terms of CPU and memory.
		MachineCfg: &proto.FirecrackerMachineConfiguration{
			VcpuCount:  2,
			MemSizeMib: 2048,
		},
		NetworkInterfaces: []*proto.FirecrackerNetworkInterface{{
			AllowMMDS: true,
			StaticConfig: &proto.StaticNetworkConfiguration{
				MacAddress:  macAddress,
				HostDevName: hostDevName,
				IPConfig: &proto.IPConfiguration{
					PrimaryAddr: orch.networkManager.GetConfig(vmID).GetContainerCIDR(),
					GatewayAddr: orch.networkManager.GetConfig(vmID).GetGatewayIP(),
					Nameservers: []string{"8.8.8.8", "1.1.1.1"},
				},
			},
		}},
		NetNS: orch.networkManager.GetConfig(vmID).GetNamespacePath(),
	}

	log.Println("Creating firecracker VM")
	_, err := orch.fcClient.CreateVM(orch.ctx, createVMRequest)
	if err != nil {
		return fmt.Errorf("creating firecracker VM: %w", err)
	}

	return nil
}

func (orch *Orchestrator) startContainer(vmID, snapKey, imageName string, image *containerd.Image) error {
	log.Println("Creating new container")
	ctr, err := orch.client.NewContainer(
		orch.ctx,
		snapKey,
		containerd.WithSnapshotter(orch.snapshotter),
		containerd.WithNewSnapshot(snapKey, *image),
		containerd.WithNewSpec(
			// We can't use the regular oci.WithImageConfig from containerd
			// because it will attempt to get UIDs and GIDs from inside the
			// container by mounting the container's filesystem. With remote
			// snapshotters, that filesystem is inside a VM and inaccessible
			// to the host. The firecrackeroci variation instructs the
			// firecracker-containerd agent that runs inside the VM to perform
			// those UID/GID lookups because it has access to the container's filesystem
			firecrackeroci.WithVMLocalImageConfig(*image),
			firecrackeroci.WithVMID(vmID),
			firecrackeroci.WithVMNetwork,
		),
		containerd.WithRuntime("aws.firecracker", nil),
	)
	if err != nil {
		return fmt.Errorf("creating new container: %w", err)
	}

	log.Println("Creating new container task")
	task, err := ctr.NewTask(orch.ctx, cio.NewCreator(cio.WithStreams(nil, nil, nil)))
	if err != nil {
		return fmt.Errorf("creating new container task: %w", err)
	}

	log.Println("Starting container task")
	if err := task.Start(orch.ctx); err != nil {
		return fmt.Errorf("starting container task: %w", err)
	}

	snapMount, err := orch.getSnapMount(snapKey)
	if err != nil {
		return fmt.Errorf("getting snapshot's disk device path: %w", err)
	}

	// Store snapshot info
	orch.vms[vmID] = VMInfo{
		imgName:            imageName,
		ctrSnapKey:         snapKey,
		containerSnapMount: snapMount,
		snapBooted:         false,
		ctr:                ctr,
		task:               task,
	}
	return nil // TODO: pass vm IP (Natted one) to CRI?
}

func (orch *Orchestrator) createSnapshot(vmID, revision string) error {
	vmInfo := orch.vms[vmID]

	log.Println("Pausing VM")
	if _, err := orch.fcClient.PauseVM(orch.ctx, &proto.PauseVMRequest{VMID: vmID}); err != nil {
		return fmt.Errorf("pausing VM: %w", err)
	}

	snap, err := orch.snapshotManager.InitSnapshot(revision, vmInfo.imgName)
	if err != nil {
		return fmt.Errorf("adding snapshot to snapshot manager: %w", err)
	}

	log.Println("Creating VM snapshot")
	createSnapshotRequest := &proto.CreateSnapshotRequest{
		VMID:         vmID,
		MemFilePath:  snap.GetMemFilePath(),
		SnapshotPath: snap.GetSnapshotFilePath(),
	}
	if _, err := orch.fcClient.CreateSnapshot(orch.ctx, createSnapshotRequest); err != nil {
		return fmt.Errorf("creating VM snapshot: %w", err)
	}

	log.Println("Resuming VM")
	if _, err := orch.fcClient.ResumeVM(orch.ctx, &proto.ResumeVMRequest{VMID: vmID}); err != nil {
		return fmt.Errorf("resuming VM: %w", err)
	}

	log.Println("Digging holes in guest memory file")
	if err := digHoles(snap.GetMemFilePath()); err != nil {
		return fmt.Errorf("digging holes in guest memory file: %w", err)
	}

	log.Println("Serializing snapshot information")
	if err := snap.SerializeSnapInfo(); err != nil {
		return fmt.Errorf("serializing snapshot information: %w", err)
	}

	if orch.useRemoteStorage {
		log.Println("Uploading snapshot to remote storage")
		
		err := orch.snapshotManager.UploadSnapshot(revision)
		if err != nil {
			return fmt.Errorf("uploading snapshot to remote storage: %w", err)
		}
	}

	return nil
}

func (orch *Orchestrator) bootVMFromSnapshot(vmID, revision string) error {	
	if _, err := orch.snapshotManager.AcquireSnapshot(revision); err != nil {
		if orch.useRemoteStorage {
			log.Println("Downloading snapshot from remote storage")

			_, err := orch.snapshotManager.DownloadSnapshot(revision)
			if err != nil {
				return fmt.Errorf("failed to download snapshot from remote storage: %w", err)
			}
		} else {
			return fmt.Errorf("snapshot %s not found locally and remote storage is disabled", revision)
		}
	}

	snap, err := orch.snapshotManager.AcquireSnapshot(revision)
	if err != nil {
		return fmt.Errorf("failed to acquire snapshot", err)
	}

	createVMRequest := &proto.CreateVMRequest{
		VMID: vmID,
		// Enabling Go Race Detector makes in-microVM binaries heavy in terms of CPU and memory.
		MachineCfg: &proto.FirecrackerMachineConfiguration{
			VcpuCount:  2,
			MemSizeMib: 2048,
		},
		NetworkInterfaces: []*proto.FirecrackerNetworkInterface{{
			StaticConfig: &proto.StaticNetworkConfiguration{
				MacAddress:  macAddress,
				HostDevName: hostDevName,
				IPConfig: &proto.IPConfiguration{
					PrimaryAddr: orch.networkManager.GetConfig(vmID).GetContainerCIDR(),
					GatewayAddr: orch.networkManager.GetConfig(vmID).GetGatewayIP(),
					Nameservers: []string{"8.8.8.8"},
				},
			},
		}},
		NetNS:        orch.networkManager.GetConfig(vmID).GetNamespacePath(),
		LoadSnapshot: true,
		MemFilePath:  snap.GetMemFilePath(),
		SnapshotPath: snap.GetSnapshotFilePath(),
	}

	log.Println("Creating firecracker VM from snapshot")
	_, err = orch.fcClient.CreateVM(orch.ctx, createVMRequest)
	if err != nil {
		return fmt.Errorf("creating firecracker VM: %w", err)
	}

	return nil
}

func (orch *Orchestrator) stopVm(vmID string) error {
	vmInfo := orch.vms[vmID]

	if !vmInfo.snapBooted {
		fmt.Println("Killing task")
		if err := vmInfo.task.Kill(orch.ctx, syscall.SIGKILL); err != nil {
			return errors.Wrapf(err, "killing task")
		}

		fmt.Println("Waiting for task to exit")
		exitStatusChannel, err := vmInfo.task.Wait(orch.ctx)
		if err != nil {
			return fmt.Errorf("getting container task exit code channel: %w", err)
		}

		<-exitStatusChannel

		fmt.Println("Deleting task")
		if _, err := vmInfo.task.Delete(orch.ctx); err != nil {
			return errors.Wrapf(err, "failed to delete task")
		}

		fmt.Println("Deleting container")
		if err := vmInfo.ctr.Delete(orch.ctx, containerd.WithSnapshotCleanup); err != nil {
			return errors.Wrapf(err, "failed to delete container")
		}
	}

	fmt.Println("Stopping VM")
	if _, err := orch.fcClient.StopVM(orch.ctx, &proto.StopVMRequest{VMID: vmID}); err != nil {
		log.Printf("failed to stop the vm")
		return err
	}

	if vmInfo.snapBooted {
		fmt.Println("Removing snapshot")
		err := orch.snapshotService.Remove(orch.ctx, vmInfo.ctrSnapKey)
		if err != nil {
			log.Printf("failed to deactivate container snapshot")
			return err
		}
		if err := orch.leaseManager.Delete(orch.ctx, *orch.leases[vmInfo.ctrSnapKey]); err != nil {
			return err
		}
		delete(orch.leases, vmInfo.ctrSnapKey)
	}

	fmt.Println("Removing network")
	if err := orch.networkManager.RemoveNetwork(vmID); err != nil {
		log.Printf("failed to cleanup network")
		return err
	}

	return nil
}

func (orch *Orchestrator) tearDown() {
	orch.client.Close()
	orch.fcClient.Close()
}

func (orch *Orchestrator) getSnapMount(snapKey string) (*mount.Mount, error) {
	mounts, err := orch.snapshotService.Mounts(orch.ctx, snapKey)
	if err != nil {
		return nil, err
	}
	if len(mounts) != 1 {
		log.Panic("expected snapshot to only have one mount")
	}

	// Devmapper always only has a single mount /dev/mapper/fc-thinpool-snap-x
	return &mounts[0], nil
}

func digHoles(filePath string) error {
	cmd := exec.Command("sudo", "fallocate", "--dig-holes", filePath)
	err := cmd.Run()
	if err != nil {
		return errors.Wrapf(err, "digging holes in %s", filePath)
	}
	return nil
}
