package snapshotting

import (
	"context"
	"github.com/pkg/errors"
	"io"
	"os"
	"path/filepath"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	s "github.com/vhive-serverless/vhive/snapshotting"
)

// Extends the original SnapshotManager to support MinIO storage
type SnapshotManager struct {
	*s.SnapshotManager // Embed the original SnapshotManager

	minioClient *minio.Client
	bucketName  string
}

func NewSnapshotManager(baseFolder, minioEndpoint, accessKey, secretKey, bucket string, useRemoteStorage bool) (*SnapshotManager, error) {
	originalManager := s.NewSnapshotManager(baseFolder)	
	manager := &SnapshotManager{
		SnapshotManager: originalManager, // Assign the original manager
	}

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

func (mgr *SnapshotManager) UploadSnapshot(revision string) error {
	snap, err := mgr.AcquireSnapshot(revision)
	if err != nil {
		return errors.Wrapf(err, "acquiring snapshot")
	}

	files := []string{
		snap.GetMemFilePath(),
		snap.GetSnapshotFilePath(),
		snap.GetPatchFilePath(),
		snap.GetInfoFilePath(),
	}
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

func (mgr *SnapshotManager) DownloadSnapshot(revision string) (*s.Snapshot, error) {
	snap, err := mgr.InitSnapshot(revision, "") // TODO: find a way to update the snapshot
	if err != nil {
		return nil, errors.Wrapf(err, "initializing snap")
	}

	files := []string{
		snap.GetMemFilePath(),
		snap.GetSnapshotFilePath(),
		snap.GetPatchFilePath(),
		snap.GetInfoFilePath(),
	}
	for _, filePath := range files {
		outFile, err := os.Create(filePath)
		if err != nil {
			return nil, errors.Wrapf(err, "creating file %s", filePath)
		}
		defer outFile.Close() // Ensure the file is closed
		fileName := filepath.Base(filePath)

		// Download the object from MinIO
		obj, err := mgr.minioClient.GetObject(context.Background(), mgr.bucketName, fileName, minio.GetObjectOptions{})
		if err != nil {
			return nil, errors.Wrapf(err, "downloading %s from MinIO", fileName)
		}

		// Copy the object to the local file
		if _, err := io.Copy(outFile, obj); err != nil {
			return nil, errors.Wrapf(err, "writing file %s", filePath)
		}
	}

	mgr.CommitSnapshot(revision)

	return snap, nil
}
