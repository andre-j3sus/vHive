package snapshotting

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Use K_REVISION environment variable as identifier for snapshot (https://github.com/amohoste/podspeed-vhive/blob/c74ca6ced1579d1c4f5414f3a28a8ffceb7b544f/pkg/pod/types/vhive.go#L46)
type SnapshotManager struct {
	snapshots        map[string]Snapshot // maps revision id to snapshot
	availableSizeMiB string
	BasePath         string

	minioClient *minio.Client
	bucketName  string
}

func NewSnapshotManager(baseFolder, minioEndpoint, accessKey, secretKey, bucket string, useRemoteStorage bool) (*SnapshotManager, error) {
	manager := new(SnapshotManager)
	manager.snapshots = make(map[string]Snapshot)
	manager.BasePath = baseFolder

	if useRemoteStorage {
		minioClient, err := minio.New(minioEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
			Secure: false,
		})
		if err != nil {
			return nil, errors.Wrap(err, "creating MinIO client")
		}
		manager.minioClient = minioClient
		manager.bucketName = bucket
	}
	
	return manager, nil
}

func (mgr *SnapshotManager) GetSnapshot(revision string) (*Snapshot, error) {
	snap, present := mgr.snapshots[revision]
	if present {
		return &snap, nil
	} else {
		return nil, errors.New(fmt.Sprintf("Get: Snapshot for revision %s does not exist", revision))
	}
}

func (mgr *SnapshotManager) UploadSnapshot(revision string) error {
	snap, exists := mgr.snapshots[revision]
	if !exists {
		return fmt.Errorf("snapshot %s does not exist", revision)
	}

	files := []string{snap.GetMemFilePath(), snap.GetSnapFilePath(), snap.GetInfoFilePath()}
	for _, file := range files {
		fileReader, err := os.Open(file)
		if err != nil {
			return errors.Wrapf(err, "opening file %s", file)
		}
		defer fileReader.Close()

		_, err = mgr.minioClient.PutObject(context.Background(), mgr.bucketName, filepath.Base(file), fileReader, -1, minio.PutObjectOptions{})
		if err != nil {
			return errors.Wrapf(err, "uploading %s to MinIO", file)
		}
	}
	return nil
}

func (mgr *SnapshotManager) DownloadSnapshot(revision string) error {
	snap := NewSnapshot(revision, mgr.BasePath)

	files := []string{"memfile", "snapfile", "infofile"}
	for _, file := range files {
		filePath := filepath.Join(snap.GetBaseFolder(), file)
		outFile, err := os.Create(filePath)
		if err != nil {
			return errors.Wrapf(err, "creating file %s", filePath)
		}
		defer outFile.Close()

		obj, err := mgr.minioClient.GetObject(context.Background(), mgr.bucketName, file, minio.GetObjectOptions{})
		if err != nil {
			return errors.Wrapf(err, "downloading %s from MinIO", file)
		}

		if _, err := io.Copy(outFile, obj); err != nil {
			return errors.Wrapf(err, "writing file %s", filePath)
		}
	}
	mgr.snapshots[revision] = snap
	return nil
}

func (mgr *SnapshotManager) RegisterSnap(revision string) (*Snapshot, error) {
	if _, present := mgr.snapshots[revision]; present {
		return nil, errors.New(fmt.Sprintf("Add: Snapshot for revision %s already exists", revision))
	}
	snap := NewSnapshot(revision, mgr.BasePath)

	err := os.Mkdir(snap.GetBaseFolder(), 0755)
	if err != nil {
		return nil, errors.Wrapf(err, "creating folder for snapshots %s", revision)
	}

	mgr.snapshots[revision] = snap
	return &snap, nil
}

func (mgr *SnapshotManager) RemoveSnapshot(revision string) error {
	snapshot, present := mgr.snapshots[revision]
	if !present {
		return errors.New(fmt.Sprintf("Delete: Snapshot for revision %s does not exist", revision))
	}

	err := os.RemoveAll(snapshot.GetBaseFolder())
	delete(mgr.snapshots, revision)

	if err != nil {
		return errors.Wrapf(err, "removing snapshot folder %s", snapshot.GetBaseFolder())
	}

	return nil
}

// Doesn't check if correct files in folders!
func (mgr *SnapshotManager) RecoverSnapshots(basePath string) error {
	files, err := ioutil.ReadDir(basePath)
	if err != nil {
		return errors.Wrapf(err, "reading folders in %s", basePath)
	}

	for _, f := range files {
		if f.IsDir() {
			revision := f.Name()
			mgr.snapshots[revision] = NewSnapshot(revision, mgr.BasePath)
			if err != nil {
				return errors.Wrapf(err, "recovering snapshot %s", f.Name())
			}
		}
	}
	return nil
}
