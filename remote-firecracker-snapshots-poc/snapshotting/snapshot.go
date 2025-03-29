package snapshotting

import (
	"encoding/gob"
	"github.com/pkg/errors"
	"os"

	s "github.com/vhive-serverless/vhive/snapshotting"
)

// RemoteSnapshot extends the original Snapshot to support remote storage and deduplication
type RemoteSnapshot struct {
	*s.Snapshot // Embed the original Snapshot

	MemFileChunks []string // Store chunk hashes for the memfile
	MemFileSize   int64    // Total size of the memfile
}

// SerializeSnapInfo serializes the snapshot info using gob.
func (snp *RemoteSnapshot) SerializeSnapInfo() error {
	file, err := os.Create(snp.GetInfoFilePath())
	if err != nil {
		return errors.Wrapf(err, "failed to create snapinfo file")
	}
	defer file.Close()

	encoder := gob.NewEncoder(file)

	err = encoder.Encode(*snp)
	if err != nil {
		return errors.Wrapf(err, "failed to encode snapinfo")
	}
	return nil
}

// LoadSnapInfo loads the snapshot info from a file.
func (snp *RemoteSnapshot) LoadSnapInfo(infoPath string) error {
	file, err := os.Open(infoPath)
	if err != nil {
		return errors.Wrapf(err, "failed to open snapinfo file")
	}
	defer file.Close()

	encoder := gob.NewDecoder(file)

	err = encoder.Decode(snp)
	if err != nil {
		return errors.Wrapf(err, "failed to decode snapinfo")
	}

	return nil
}
