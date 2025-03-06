package snapshotting

import (
	"context"
	"encoding/hex"
	"crypto/sha256"
	"github.com/pkg/errors"
	"io"
	"os"
	"fmt"
	"bytes"
	"time"
	"path/filepath"

	"github.com/redis/go-redis/v9"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	s "github.com/vhive-serverless/vhive/snapshotting"
)


const ChunkSize = 1024 * 1024 // 1MB chunks

// RemoteSnapshotManager extends the original SnapshotManager to support remote storage using MinIO and Redis
// MinIO is used to store the snapshot files, while Redis is used to store chunk hashes for deduplication
type RemoteSnapshotManager struct {
	*s.SnapshotManager // Embed the original SnapshotManager

	redisClient *redis.Client
	minioClient *minio.Client
	bucketName  string
}

// Create a new RemoteSnapshotManager with the specified configuration
func NewRemoteSnapshotManager(baseFolder, minioEndpoint, accessKey, secretKey, bucketName, redisAddr string, useRemoteStorage bool) (*RemoteSnapshotManager, error) {
	originalManager := s.NewSnapshotManager(baseFolder)	
	manager := &RemoteSnapshotManager{
		SnapshotManager: originalManager, // Assign the original manager
	}

	if useRemoteStorage {
		manager.redisClient = redis.NewClient(&redis.Options{
			Addr: redisAddr,
			Password: "", // no password set TODO: either receive connection URL or password in constructor
			DB:		  0,  // use default DB
		})

		minioClient, err := minio.New(minioEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
			Secure: false,
		})
		if err != nil {
			return nil, errors.Wrap(err, "creating MinIO client")
		}
		manager.minioClient = minioClient
		manager.bucketName = bucketName

		// Ensure bucket exists
		exists, err := minioClient.BucketExists(context.Background(), bucketName)
		if err != nil {
			return nil, errors.Wrap(err, "checking bucket existence")
		}
		if !exists {
			err = minioClient.MakeBucket(context.Background(), bucketName, minio.MakeBucketOptions{})
			if err != nil {
				return nil, errors.Wrap(err, "creating bucket")
			}
		}
	}
	
	return manager, nil
}

func (mrg *RemoteSnapshotManager) AcquireSnapshot(revision string) (*RemoteSnapshot, error) {
	snap, err := mrg.SnapshotManager.AcquireSnapshot(revision)
	if err != nil {
		return nil, errors.Wrap(err, "acquiring snapshot")
	}

	return &RemoteSnapshot{
		Snapshot: snap,
	}, nil
}

// Upload a snapshot to MinIO
// The snapshot memfile is chunked and uploaded to MinIO, while other files are uploaded directly
// A manifest is created and uploaded to MinIO to describe the snapshot contents
func (mgr *RemoteSnapshotManager) UploadSnapshot(revision string) error {
	snap, err := mgr.AcquireSnapshot(revision)
	if err != nil {
		return errors.Wrapf(err, "acquiring snapshot")
	}

	// Deduplicate and upload memfile chunks
	memFile := snap.GetMemFilePath()
	chunks, totalSize, err := mgr.chunkAndUploadFile(memFile)
	if err != nil {
		return errors.Wrapf(err, "processing memfile")
	}
	snap.MemFileChunks = chunks
	snap.MemFileSize = totalSize

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
func (mgr *RemoteSnapshotManager) chunkAndUploadFile(filePath string) ([]string, int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, 0, errors.Wrap(err, "opening file")
	}
	defer file.Close()

	var totalSize int64
	manifest := []string{}
	buffer := make([]byte, ChunkSize)

	for {
		bytesRead, err := file.Read(buffer)
		if err != nil && err != io.EOF {
			return nil, 0, errors.Wrap(err, "reading file")
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
			return nil, 0, errors.Wrap(err, "checking Redis for chunk" + chunkHash)
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
				return nil, 0, errors.Wrap(err, "uploading chunk to MinIO")
			}
			
			// Mark chunk as stored in Redis with TTL
			err = mgr.redisClient.Set(context.Background(), chunkHash, "stored", 24*time.Hour).Err()
			if err != nil {
				return nil, 0, errors.Wrap(err, "updating Redis")
			}
		}
		manifest = append(manifest, chunkHash)
	}
	return manifest, totalSize, nil
}

// Download a snapshot from MinIO and reconstruct it on the local filesystem
func (mgr *RemoteSnapshotManager) DownloadSnapshot(revision string) (*RemoteSnapshot, error) {
	baseSnap, err := mgr.InitSnapshot(revision, "")
	if err != nil {
		return nil, errors.Wrapf(err, "initializing snapshot")
	}

	// Create a RemoteSnapshot embedding the base Snapshot
	snap := &RemoteSnapshot{
		Snapshot: baseSnap,
	}
	
	// Download and parse info file
	info_file, err := mgr.minioClient.GetObject(
		context.Background(),
		mgr.bucketName,
		fmt.Sprintf("%s/%s", revision, filepath.Base(snap.GetInfoFilePath())),
		minio.GetObjectOptions{},
	)
	if err != nil {
		return nil, errors.Wrap(err, "downloading manifest")
	}
	defer info_file.Close()

	outFile, err := os.Create(snap.GetInfoFilePath())
	if err != nil {
		return nil, errors.Wrap(err, "creating output file")
	}
	defer outFile.Close()
	if _, err := io.Copy(outFile, info_file); err != nil {
		return nil,  errors.Wrap(err, "writing file")
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
		defer outFile.Close() // Ensure the file is closed
		fileName := filepath.Base(filePath)

		if filePath == snap.GetMemFilePath() {
			// Reconstruct the memfile from chunks
			if err := mgr.downloadChunkedFile(filePath, snap.MemFileChunks, snap.MemFileSize); err != nil {
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
func (mgr *RemoteSnapshotManager) downloadChunkedFile(filePath string, chunkHashes []string, fileSize int64) error {
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
func (mgr *RemoteSnapshotManager) downloadFile(revision, filePath, fileName string) error {
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