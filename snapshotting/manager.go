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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/redis/go-redis/v9"

	log "github.com/sirupsen/logrus"
)

const ChunkSize = 4096

// SnapshotManager manages snapshots stored on the node.
type SnapshotManager struct {
	sync.Mutex
	// Stored snapshots (identified by the function instance revision, which is provided by the `K_REVISION` environment
	// variable of knative).
	snapshots  map[string]*Snapshot
	baseFolder string

	// MinIO is used to store remote snapshots, while Redis is used to lookup deduplication hashes
	minioClient *minio.Client
	bucketName  string
	redisClient *redis.Client
}

// Snapshot identified by VM id

func NewSnapshotManager(baseFolder, bucketName string, minioClient *minio.Client, redisClient *redis.Client) *SnapshotManager {
	manager := new(SnapshotManager)
	manager.snapshots = make(map[string]*Snapshot)
	manager.baseFolder = baseFolder

	// Clean & init basefolder
	_ = os.RemoveAll(manager.baseFolder)
	_ = os.MkdirAll(manager.baseFolder, os.ModePerm)

	if minioClient != nil && redisClient != nil && bucketName != "" {
		manager.bucketName = bucketName
		manager.minioClient = minioClient
		manager.redisClient = redisClient

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
func (mgr *SnapshotManager) InitSnapshot(revision, image, vmID string) (*Snapshot, error) {
	mgr.Lock()

	logger := log.WithFields(log.Fields{"revision": revision, "image": image})
	logger.Debug("Initializing snapshot corresponding to revision and image")

	if _, present := mgr.snapshots[revision]; present {
		mgr.Unlock()
		return nil, errors.New(fmt.Sprintf("Add: Snapshot for revision %s already exists", revision))
	}

	// Create snapshot object and move into creating state
	snap := NewSnapshot(revision, mgr.baseFolder, image, vmID)
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

// Upload a snapshot to MinIO
// The snapshot memfile is chunked and uploaded to MinIO, while other files are uploaded directly
// A manifest is created and uploaded to MinIO to describe the snapshot contents
func (mgr *SnapshotManager) UploadSnapshot(revision string) error {
	snap, err := mgr.AcquireSnapshot(revision)
	if err != nil {
		return errors.Wrapf(err, "acquiring snapshot")
	}

	// Deduplicate and upload memfile chunks
	memFile := snap.GetMemFilePath()
	chunks, _, err := mgr.chunkAndUploadFile(memFile)
	if err != nil {
		return errors.Wrapf(err, "processing memfile")
	}
	snap.MemFileChunks = chunks

	if err := snap.SerializeSnapInfo(); err != nil {
		return fmt.Errorf("serializing snapshot information: %w", err)
	}

	// Handle other files directly without chunking
	otherFiles := []string{
		snap.GetSnapshotFilePath(),
		snap.GetInfoFilePath(),
	}

	for _, filePath := range otherFiles {
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

// Chunk a file and upload each chunk to MinIO, returning the list of chunk hashes and the total size of the file
func (mgr *SnapshotManager) chunkAndUploadFile(filePath string) ([]string, int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, 0, errors.Wrapf(err, "opening file %s", filePath)
	}
	defer file.Close()

	var totalSize int64
	manifest := []string{}
	buffer := make([]byte, ChunkSize)

	for {
		bytesRead, err := file.Read(buffer)
		if err != nil && err != io.EOF {
			return nil, 0, errors.Wrapf(err, "reading file %s", filePath)
		}
		if bytesRead == 0 {
			break
		}
		totalSize += int64(bytesRead)

		chunkData := buffer[:bytesRead]
		chunkHash := hashChunk(chunkData)

		// Check if chunk exists in Redis
		exists, err := mgr.redisClient.Exists(context.Background(), chunkHash).Result()
		if err != nil {
			return nil, 0, errors.Wrapf(err, "checking Redis for chunk %s", chunkHash)
		}

		if exists == 0 {
			// Upload chunk to MinIO
			_, err := mgr.minioClient.PutObject(
				context.Background(),
				mgr.bucketName,
				chunkHash,
				bytes.NewReader(chunkData),
				int64(bytesRead),
				minio.PutObjectOptions{},
			)
			if err != nil {
				return nil, 0, errors.Wrapf(err, "uploading chunk %s", chunkHash)
			}

			// Mark chunk as stored in Redis with TTL
			err = mgr.redisClient.Set(context.Background(), chunkHash, "stored", 24*time.Hour).Err()
			if err != nil {
				return nil, 0, errors.Wrapf(err, "storing chunk %s in Redis", chunkHash)
			}
		}
		manifest = append(manifest, chunkHash)
	}
	return manifest, totalSize, nil
}

// Download a snapshot from MinIO and reconstruct it on the local filesystem
func (mgr *SnapshotManager) DownloadSnapshot(revision string) (*Snapshot, error) {
	snap, err := mgr.InitSnapshot(revision, "", "")
	if err != nil {
		return nil, errors.Wrapf(err, "initializing snapshot for revision %s", revision)
	}

	// Download and parse info file
	info_file, err := mgr.minioClient.GetObject(
		context.Background(),
		mgr.bucketName,
		fmt.Sprintf("%s/%s", revision, filepath.Base(snap.GetInfoFilePath())),
		minio.GetObjectOptions{},
	)
	if err != nil {
		return nil, errors.Wrapf(err, "downloading manifest for snapshot %s", revision)
	}
	defer info_file.Close()

	log.Infof("Writing manifest to %s", snap.GetInfoFilePath())

	outFile, err := os.Create(snap.GetInfoFilePath())
	if err != nil {
		return nil, errors.Wrapf(err, "creating file %s", snap.GetInfoFilePath())
	}
	defer outFile.Close()
	log.Infof("2.0 Writing manifest to %s %s", snap.GetInfoFilePath(), outFile.Name())
	if _, err := io.Copy(outFile, info_file); err != nil {
		return nil, errors.Wrapf(err, "writing manifest to %s", snap.GetInfoFilePath())
	}

	log.Infof("Loading manifest from %s", snap.GetInfoFilePath())

	err = snap.LoadSnapInfo(snap.GetInfoFilePath())
	if err != nil {
		return nil, errors.Wrapf(err, "loading manifest from %s", snap.GetInfoFilePath())
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
		defer outFile.Close() // Ensure the file is closed
		fileName := filepath.Base(filePath)

		if filePath == snap.GetMemFilePath() {
			// Reconstruct the memfile from chunks
			if err := mgr.downloadChunkedFile(filePath, snap.MemFileChunks); err != nil {
				return nil, errors.Wrap(err, "reconstructing memfile")
			}
		} else {
			if err := mgr.downloadFile(revision, filePath, fileName); err != nil {
				return nil, errors.Wrapf(err, "downloading file %s", fileName)
			}
		}
	}

	if err := mgr.CommitSnapshot(revision); err != nil {
		return nil, errors.Wrap(err, "committing snapshot")
	}

	return snap, nil
}

// Reconstruct a chunked file by downloading each chunk from MinIO and writing it to the specified path
func (mgr *SnapshotManager) downloadChunkedFile(filePath string, chunkHashes []string) error {
	outFile, err := os.Create(filePath)
	if err != nil {
		return errors.Wrap(err, "creating output file")
	}
	defer outFile.Close()

	for _, chunkHash := range chunkHashes {
		chunk, err := mgr.minioClient.GetObject(
			context.Background(),
			mgr.bucketName,
			chunkHash,
			minio.GetObjectOptions{},
		)
		if err != nil {
			return errors.Wrapf(err, "downloading chunk %s", chunkHash)
		}

		if _, err := io.Copy(outFile, chunk); err != nil {
			chunk.Close()
			return errors.Wrapf(err, "writing chunk %s", chunkHash)
		}
		chunk.Close()
	}

	return nil
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

func hashChunk(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
