// MIT License
//
// Copyright (c) 2023 Georgiy Lebedev, Amory Hoste and vHive team
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package snapshotting

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/minio/minio-go/v7"

	log "github.com/sirupsen/logrus"
)

// SnapshotManager manages snapshots stored on the node.
type SnapshotManager struct {
	sync.Mutex
	// Stored snapshots (identified by the function instance revision, which is provided by the `K_REVISION` environment
	// variable of knative).
	snapshots  map[string]*Snapshot
	baseFolder string

	// MinIO is used to store remote snapshots
	minioClient *minio.Client
	bucketName  string
}

// Snapshot identified by VM id

func NewSnapshotManager(baseFolder, bucketName string, minioClient *minio.Client) *SnapshotManager {
	manager := new(SnapshotManager)
	manager.snapshots = make(map[string]*Snapshot)
	manager.baseFolder = baseFolder

	// Clean & init basefolder
	_ = os.RemoveAll(manager.baseFolder)
	_ = os.MkdirAll(manager.baseFolder, os.ModePerm)

	manager.bucketName = bucketName
	manager.minioClient = minioClient

	if minioClient != nil {
		// Ensure bucket exists
		exists, err := minioClient.BucketExists(context.Background(), bucketName)
		if err != nil {
			log.WithError(err).Error("Failed to check if bucket exists")
		}
		if !exists {
			err = minioClient.MakeBucket(context.Background(), bucketName, minio.MakeBucketOptions{})
			if err != nil {
				log.WithError(err).Error("Failed to create bucket")
			}
		}
	}

	return manager
}

// AcquireSnapshot returns a snapshot for the specified revision if it is available.
func (mgr *SnapshotManager) AcquireSnapshot(revision string) (*Snapshot, error) {
	mgr.Lock()
	defer mgr.Unlock()

	// Check if idle snapshot is available for the given image
	snap, ok := mgr.snapshots[revision]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Get: Snapshot for revision %s does not exist", revision))
	}

	// Snapshot registered in manager but creation not finished yet
	if !snap.ready {
		return nil, errors.New("Snapshot is not yet usable")
	}

	// Return snapshot for supplied revision
	return mgr.snapshots[revision], nil
}

// InitSnapshot initializes a snapshot by adding its metadata to the SnapshotManager. Once the snapshot has
// been created, CommitSnapshot must be run to finalize the snapshot creation and make the snapshot available for use.
func (mgr *SnapshotManager) InitSnapshot(revision, image string) (*Snapshot, error) {
	mgr.Lock()

	logger := log.WithFields(log.Fields{"revision": revision, "image": image})
	logger.Debug("Initializing snapshot corresponding to revision and image")

	if _, present := mgr.snapshots[revision]; present {
		mgr.Unlock()
		return nil, errors.New(fmt.Sprintf("Add: Snapshot for revision %s already exists", revision))
	}

	// Create snapshot object and move into creating state
	snap := NewSnapshot(revision, mgr.baseFolder, image)
	mgr.snapshots[snap.GetId()] = snap
	mgr.Unlock()

	// Create directory to store snapshot data
	err := snap.CreateSnapDir()
	if err != nil {
		return nil, errors.Wrapf(err, "creating snapDir for snapshots %s", revision)
	}

	return snap, nil
}

// DeleteSnapshot removes the snapshot for the specified revision from the manager.
func (mgr *SnapshotManager) DeleteSnapshot(revision string) error {
	mgr.Lock()
	defer mgr.Unlock()

	snap, ok := mgr.snapshots[revision]
	if !ok {
		return errors.New(fmt.Sprintf("Delete: Snapshot for revision %s does not exist", revision))
	}

	if snap.ready {
		return errors.New(fmt.Sprintf("Delete: Snapshot for revision %s has already been committed", revision))
	}

	snap.Cleanup()

	delete(mgr.snapshots, revision)

	return nil
}

// CommitSnapshot finalizes the snapshot creation and makes it available for use.
func (mgr *SnapshotManager) CommitSnapshot(revision string) error {
	mgr.Lock()
	defer mgr.Unlock()

	snap, ok := mgr.snapshots[revision]
	if !ok {
		return errors.New(fmt.Sprintf("Snapshot for revision %s to commit does not exist", revision))
	}

	if snap.ready {
		return errors.New(fmt.Sprintf("Snapshot for revision %s has already been committed", revision))
	}

	snap.ready = true

	return nil
}

// UploadSnapshot uploads a snapshot to MinIO
func (mgr *SnapshotManager) UploadSnapshot(revision string) error {
	snap, err := mgr.AcquireSnapshot(revision)
	if err != nil {
		return errors.Wrapf(err, "acquiring snapshot")
	}

	if err := snap.SerializeSnapInfo(); err != nil {
		return fmt.Errorf("serializing snapshot information: %w", err)
	}

	files := []string{
		snap.GetSnapshotFilePath(),
		snap.GetInfoFilePath(),
		snap.GetMemFilePath(),
	}

	for _, filePath := range files {
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			return errors.Wrapf(err, "getting file info for %s", filePath)
		}

		file, err := os.Open(filePath)
		if err != nil {
			return errors.Wrapf(err, "opening file %s", filePath)
		}
		defer file.Close()

		objectKey := fmt.Sprintf("%s/%s", revision, filepath.Base(filePath))
		_, err = mgr.minioClient.PutObject(
			context.Background(),
			mgr.bucketName,
			objectKey,
			file,
			fileInfo.Size(),
			minio.PutObjectOptions{},
		)
		if err != nil {
			return errors.Wrapf(err, "uploading file %s", filePath)
		}
	}

	return nil
}

// DownloadSnapshot downloads a snapshot from MinIO
func (mgr *SnapshotManager) DownloadSnapshot(revision string) (*Snapshot, error) {
	snap, err := mgr.InitSnapshot(revision, "")
	if err != nil {
		return nil, errors.Wrapf(err, "initializing snapshot")
	}

	// Download and parse info file
	infoFile, err := mgr.minioClient.GetObject(
		context.Background(),
		mgr.bucketName,
		fmt.Sprintf("%s/%s", revision, filepath.Base(snap.GetInfoFilePath())),
		minio.GetObjectOptions{},
	)
	if err != nil {
		return nil, errors.Wrap(err, "downloading manifest")
	}
	defer infoFile.Close()

	outFile, err := os.Create(snap.GetInfoFilePath())
	if err != nil {
		return nil, errors.Wrap(err, "creating output file")
	}
	defer outFile.Close()
	if _, err := io.Copy(outFile, infoFile); err != nil {
		return nil, errors.Wrap(err, "writing file")
	}

	err = snap.LoadSnapInfo(snap.GetInfoFilePath())
	if err != nil {
		return nil, errors.Wrap(err, "loading snapshot info")
	}

	files := []string{
		snap.GetSnapshotFilePath(),
		snap.GetMemFilePath(),
	}
	for _, filePath := range files {
		outFile, err := os.Create(filePath)
		if err != nil {
			return nil, errors.Wrapf(err, "creating file %s", filePath)
		}
		defer outFile.Close()
		fileName := filepath.Base(filePath)

		if err := mgr.downloadFile(revision, filePath, fileName); err != nil {
			return nil, errors.Wrapf(err, "downloading file %s", fileName)
		}
	}

	if err := mgr.CommitSnapshot(revision); err != nil {
		return nil, errors.Wrap(err, "committing snapshot")
	}

	return snap, nil
}

// Download a file from MinIO and save it to the specified path
func (mgr *SnapshotManager) downloadFile(revision, filePath, fileName string) error {
	outFile, err := os.Create(filePath)
	if err != nil {
		return errors.Wrap(err, "creating output file")
	}
	defer outFile.Close()

	objectKey := fmt.Sprintf("%s/%s", revision, fileName)
	obj, err := mgr.minioClient.GetObject(
		context.Background(),
		mgr.bucketName,
		objectKey,
		minio.GetObjectOptions{},
	)
	if err != nil {
		return errors.Wrap(err, "downloading from MinIO")
	}
	defer obj.Close()

	if _, err := io.Copy(outFile, obj); err != nil {
		return errors.Wrap(err, "writing file")
	}

	return nil
}
