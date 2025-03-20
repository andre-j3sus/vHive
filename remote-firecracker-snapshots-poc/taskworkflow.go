package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"sync"
	"bufio"

	"github.com/containerd/containerd/reference"
)

const (
	containerdAddress      = "/run/firecracker-containerd/containerd.sock"
	containerdTTRPCAddress = containerdAddress + ".ttrpc"
	namespaceName          = "remote-firecracker-snapshots-poc"
	macAddress             = "AA:FC:00:00:00:01"
	hostDevName            = "tap0"
	snapshotter            = "proxy"
	dockerMetadataTemplate = `
		{
			"docker-credentials": {
				"%s": {
					"username": "%s", 
					"password": "%s"
				}
			}
		}`
)

var once sync.Once // Ensures tearDownVM is only called once

func main() {
	var vmID = flag.String("id", "", "virtual machine identifier")
	var image = flag.String("image", "", "container image name")
	var revision = flag.String("revision", "", "revision identifier")
	var snapsBasePath = flag.String("snapshots-base-path", "", "base path for snapshots")
	var keepalive = flag.Int("keepalive", 3600, "keepalive timeout")
	var makeSnap = flag.Bool("make-snap", false, "bootstrap and make a snapshot")
	var bootFromSnap = flag.Bool("boot-from-snap", false, "boot from snapshot")
	
	// MinIO-related flags for remote snapshot storage 
	var useRemoteStorage = flag.Bool("use-remote-storage", false, "store snapshots remotely")
	var minioEndpoint = flag.String("minio-endpoint", "localhost:9000", "MinIO server endpoint")
	var accessKey = flag.String("minio-access-key", "ROOTUSER", "MinIO access key")
	var secretKey = flag.String("minio-secret-key", "CHANGEME123", "MinIO secret key")
	var bucket = flag.String("minio-bucket", "snapshots", "MinIO bucket name")
	var redisAddr = flag.String("redis-addr", "localhost:6379", "MinIO bucket name")

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	flag.Parse()

	if *vmID == "" {
		log.Fatal("Incorrect usage. 'id' needs to be specified")
	}

	if *image == "" && !*bootFromSnap {
		log.Fatal("Incorrect usage. 'image' needs to be specified when 'boot-snap' is false")
	}

	if *snapsBasePath == "" {
		log.Fatal("Incorrect usage. 'snapshots-base' needs to be specified")
	}

	if *revision == "" {
		log.Fatal("Incorrect usage. 'revision' needs to be specified")
	}

	if err := taskWorkflow(*vmID, *image, *revision, *snapsBasePath, *keepalive, *makeSnap, *bootFromSnap, *useRemoteStorage, *minioEndpoint, *accessKey, *secretKey, *bucket, *redisAddr); err != nil {
		log.Fatal(err)
	}
}

func taskWorkflow(vmID, image, revision, snapsBasePath string, keepAlive int, makeSnap, bootFromSnap, useRemoteStorage bool, minioEndpoint, accessKey, secretKey, bucket, redisAddr string) error {
	log.Println("Creating orchestrator")
	// The example http-address-resolver assumes that the containerd namespace
	// is the sames as the VM ID.
	orch, err := NewOrchestrator(snapshotter, vmID, snapsBasePath, minioEndpoint, accessKey, secretKey, bucket, redisAddr, useRemoteStorage)
	if err != nil {
		return fmt.Errorf("creating orchestrator: %w", err)
	}

	log.Println("Creating network")
	if _, err := orch.networkManager.CreateNetwork(vmID); err != nil {
		return fmt.Errorf("creating network: %w", err)
	}

	if !bootFromSnap {
		log.Println("Bootstrapping VM")
		err = bootstrapVM(orch, vmID, image)
		if err != nil {
			return fmt.Errorf("bootstrapping VM: %w", err)
		}
	} else {
		log.Println("Booting VM from snapshot")
		err = bootVMFromSnapshot(orch, vmID, revision)
		if err != nil {
			return fmt.Errorf("booting VM from snapshot: %w", err)
		}
	}
	fmt.Printf("VM available at IP: %s\n", orch.networkManager.GetConfig(vmID).GetCloneIP())
	time.Sleep(3 * time.Second)

	SetupCloseHandler(orch, vmID)

	fmt.Print("Press Enter to continue...")
	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadString('\n') // Waits for Enter key press

	if makeSnap {
		log.Println("Creating VM snapshot")
		time.Sleep(3 * time.Second)
		err = orch.createSnapshot(vmID, revision)
		if err != nil {
			return fmt.Errorf("creating VM snapshot: %w", err)
		}
		log.Println("Snapshot created")
	}

	time.Sleep(time.Duration(keepAlive) * time.Second)
	log.Println("Tearing down VM")
	once.Do(func() { // Ensures tearDownVM is only executed once
		if err := tearDownVM(orch, vmID); err != nil {
			log.Fatalf("Error tearing down VM: %v", err)
		}
	})
	log.Println("VM successfully shut down")

	return nil
}

func bootstrapVM(orch *Orchestrator, vmID, imageName string) error {
	refSpec, err := reference.Parse(imageName)
	if err != nil {
		fmt.Printf("%s is not a valid image reference\n", imageName)
	}
	dockerHost := refSpec.Hostname()
	dockerUser, ok := os.LookupEnv("DOCKER_USERNAME")
	if !ok {
		fmt.Print("Docker username: ")
		_, err := fmt.Scanln(&dockerUser)
		if err != nil {
			return fmt.Errorf("docker username: %w", err)
		}
	}
	dockerPass, ok := os.LookupEnv("DOCKER_PASSWORD")
	if !ok {
		fmt.Print("Docker password: ")
		_, err := fmt.Scanln(&dockerPass)
		if err != nil {
			return fmt.Errorf("docker password: %w", err)
		}
	}

	log.Println("Creating VM")
	err = orch.createVM(vmID)
	if err != nil {
		return fmt.Errorf("creating VM: %w", err)
	}

	log.Println("Retrieving container image")
	image, err := orch.getContainerImage(vmID, imageName, dockerMetadataTemplate, dockerHost, dockerUser, dockerPass)
	if err != nil {
		return fmt.Errorf("getting container image: %w", err)
	}

	log.Println("Starting container")
	err = orch.startContainer(vmID, getSnapKey(vmID), imageName, image)
	if err != nil {
		return fmt.Errorf("starting container: %w", err)
	}

	return nil
}

func bootVMFromSnapshot(orch *Orchestrator, vmID, revision string) error {
	log.Println("Booting VM from snapshot")
	err := orch.bootVMFromSnapshot(vmID, revision)
	if err != nil {
		return fmt.Errorf("booting VM from snapshot: %w", err)
	}

	return nil
}

func tearDownVM(orch *Orchestrator, vmID string) error {
	log.Println("Tearing down VM")

	err := orch.stopVm(vmID)
	if err != nil {
		return err
	}

	return nil
}

func SetupCloseHandler(orch *Orchestrator, vmID string) {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\r- Ctrl+C pressed in Terminal")
		once.Do(func() {
			if err := tearDownVM(orch, vmID); err != nil {
				log.Printf("Error tearing down VM: %v", err)
				os.Exit(1)
			}
		})
		os.Exit(0)
	}()
}

func getSnapKey(vmID string) string {
	return "demo-snap" + vmID
}
