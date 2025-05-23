// MIT License
//
// Copyright (c) 2023 Georgiy Lebedev, Plamen Petrov and vHive team
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

package firecracker

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testImageName = "ghcr.io/ease-lab/helloworld:var_workload"
)

var (
	coord *coordinator
)

func TestMain(m *testing.M) {
	coord = newFirecrackerCoordinator(nil, withoutOrchestrator())

	ret := m.Run()
	os.Exit(ret)
}

func TestStartStop(t *testing.T) {
	containerID := "1"
	revision := "myrev-1"
	fi, err := coord.startVM(context.Background(), testImageName, revision)
	require.NoError(t, err, "could not start VM")

	err = coord.insertActive(containerID, fi)
	require.NoError(t, err, "could not insert mapping")

	present := coord.isActive(containerID)
	require.True(t, present, "container is not active")

	err = coord.stopVM(context.Background(), containerID)
	require.NoError(t, err, "could not stop VM")

	present = coord.isActive(containerID)
	require.False(t, present, "container is active")
}

func TestParallelStartStop(t *testing.T) {
	var wg sync.WaitGroup

	containerNum := 1000

	for i := 0; i < containerNum; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			containerID := strconv.Itoa(i)
			revision := fmt.Sprintf("myrev-%d", i)
			fi, err := coord.startVM(context.Background(), testImageName, revision)
			require.NoError(t, err, "could not start VM")

			err = coord.insertActive(containerID, fi)
			require.NoError(t, err, "could not insert mapping")

			present := coord.isActive(containerID)
			require.True(t, present, "container is not active")

			err = coord.stopVM(context.Background(), containerID)
			require.NoError(t, err, "could not stop VM")

			present = coord.isActive(containerID)
			require.False(t, present, "container is active")
		}(i)
	}

	wg.Wait()
}

func TestOrchCreateSnapshot(t *testing.T) {
	containerID := "1"
	revision := "myrev-1"

	// Start VM
	fi, err := coord.startVM(context.Background(), testImageName, revision)
	require.NoError(t, err, "could not start VM")

	err = coord.insertActive(containerID, fi)
	require.NoError(t, err, "could not insert mapping")

	// Trigger snapshot creation
	err = coord.orchCreateSnapshot(context.Background(), fi)
	require.NoError(t, err, "snapshot creation failed")

	// Verify that the snapshot is available and ready
	snap, err := coord.snapshotManager.AcquireSnapshot(revision)
	require.NoError(t, err, "snapshot was not marked ready or does not exist")
	require.NotNil(t, snap, "acquired snapshot is nil")

	// Clean up
	err = coord.stopVM(context.Background(), containerID)
	require.NoError(t, err, "could not stop VM")
}
