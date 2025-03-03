// +build gofuzz

/*
   Copyright The containerd Authors.
   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at
       http://www.apache.org/licenses/LICENSE-2.0
   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

/*
	To run this fuzzer, it must first be moved to
	integration/client. OSS-fuzz does this automatically
	everytime it builds the fuzzers.
*/

package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	fuzz "github.com/AdaLogics/go-fuzz-headers"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/sys"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"time"
)

func init() {
	err := updatePathEnv()
	if err != nil {
		panic(err)
	}

}

func tearDown() error {
	if err := ctrd.Stop(); err != nil {
		if err := ctrd.Kill(); err != nil {
			return err
		}
	}
	if err := ctrd.Wait(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return err
		}
	}
	if err := sys.ForceRemoveAll(defaultRoot); err != nil {
		return err
	}

	return nil
}

// checkIfShouldRestart() checks if an error indicates that
// the daemon is not running. If the daemon is not running,
// it deletes it to allow the fuzzer to create a new and
// working socket.
func checkIfShouldRestart(err error) {
	if strings.Contains(err.Error(), "daemon is not running") {
		deleteSocket()
	}
}

// startDaemon() starts the daemon.
func startDaemon(ctx context.Context, shouldTearDown bool) {
	buf := bytes.NewBuffer(nil)
	stdioFile, err := ioutil.TempFile("", "")
	if err != nil {
		// We panic here as it is a fuzz-blocker that
		// may need fixing
		panic(err)
	}
	defer func() {
		stdioFile.Close()
		os.Remove(stdioFile.Name())
	}()
	ctrdStdioFilePath = stdioFile.Name()
	stdioWriter := io.MultiWriter(stdioFile, buf)
	err = ctrd.start("containerd", address, []string{
		"--root", defaultRoot,
		"--state", defaultState,
		"--log-level", "debug",
		"--config", createShimDebugConfig(),
	}, stdioWriter, stdioWriter)
	if err != nil {
		// We are fine if the error is that the daemon is already running,
		// but if the error is another, then it will be a fuzz blocker,
		// so we panic
		if !strings.Contains(err.Error(), "daemon is already running") {
			fmt.Fprintf(os.Stderr, "%s: %s\n", err, buf.String())
		}
	}
	if shouldTearDown {
		defer func() {
			err = tearDown()
			if err != nil {
				checkIfShouldRestart(err)
			}
		}()
	}
	seconds := 4 * time.Second
	waitCtx, waitCancel := context.WithTimeout(ctx, seconds)

	_, err = ctrd.waitForStart(waitCtx)
	waitCancel()
	if err != nil {
		ctrd.Stop()
		ctrd.Kill()
		ctrd.Wait()
		fmt.Fprintf(os.Stderr, "%s: %s\n", err, buf.String())
		return
	}
}

// deleteSocket() deletes the socket in the file system.
// This is needed because the socket occasionally will
// refuse a connection to it, and deleting it allows us
// to create a new socket when invoking containerd.New()
func deleteSocket() error {
	err := os.Remove("/run/containerd-test/containerd.sock")
	if err != nil {
		return err
	}
	return nil
}

// updatePathEnv() creates an empty directory in which
// the fuzzer will create the containerd socket.
// updatePathEnv() furthermore adds "/out/containerd-binaries"
// to $PATH, since the binaries are available there.
func updatePathEnv() error {
	// Create test dir for socket
	err := os.MkdirAll("/run/containerd-test", 0777)
	if err != nil {
		return err
	}

	oldPathEnv := os.Getenv("PATH")
	newPathEnv := oldPathEnv + ":/out/containerd-binaries"
	err = os.Setenv("PATH", newPathEnv)
	if err != nil {
		return err
	}
	return nil
}

// checkAndDoUnpack checks if an image is unpacked.
// If it is not unpacked, then we may or may not
// unpack it. The fuzzer decides.
func checkAndDoUnpack(image containerd.Image, ctx context.Context, f *fuzz.ConsumeFuzzer) {
	unpacked, err := image.IsUnpacked(ctx, testSnapshotter)
	if err == nil && unpacked {
		shouldUnpack, err := f.GetBool()
		if err == nil && shouldUnpack {
			_ = image.Unpack(ctx, testSnapshotter)
		}
	}
}

// getImage() returns an image from the client.
// The fuzzer decides which image is returned.
func getImage(client *containerd.Client, f *fuzz.ConsumeFuzzer) (containerd.Image, error) {
	images, err := client.ListImages(nil)
	if err != nil {
		return nil, err
	}
	imageIndex, err := f.GetInt()
	if err != nil {
		return nil, err
	}
	image := images[imageIndex%len(images)]
	return image, nil

}

// newContainer creates and returns a container
// The fuzzer decides how the container is created
func newContainer(client *containerd.Client, f *fuzz.ConsumeFuzzer, ctx context.Context) (containerd.Container, error) {

	// determiner determines how we should create the container
	determiner, err := f.GetInt()
	if err != nil {
		return nil, err
	}
	id, err := f.GetString()
	if err != nil {
		return nil, err
	}

	if determiner%1 == 0 {
		// Create a container with oci specs
		spec := &oci.Spec{}
		err = f.GenerateStruct(spec)
		if err != nil {
			return nil, err
		}
		container, err := client.NewContainer(ctx, id,
			containerd.WithSpec(spec))
		if err != nil {
			return nil, err
		}
		return container, nil
	} else if determiner%2 == 0 {
		// Create a container with fuzzed oci specs
		// and an image
		image, err := getImage(client, f)
		if err != nil {
			return nil, err
		}
		// Fuzz a few image APIs
		_, _ = image.Size(ctx)
		checkAndDoUnpack(image, ctx, f)

		spec := &oci.Spec{}
		err = f.GenerateStruct(spec)
		if err != nil {
			return nil, err
		}
		container, err := client.NewContainer(ctx,
			id,
			containerd.WithImage(image),
			containerd.WithSpec(spec))
		if err != nil {
			return nil, err
		}
		return container, nil
	} else {
		// Create a container with an image
		image, err := getImage(client, f)
		if err != nil {
			return nil, err
		}
		// Fuzz a few image APIs
		_, _ = image.Size(ctx)
		checkAndDoUnpack(image, ctx, f)

		container, err := client.NewContainer(ctx,
			id,
			containerd.WithImage(image))
		if err != nil {
			return nil, err
		}
		return container, nil
	}
	return nil, errors.New("Could not create container")
}

// doFuzz() implements the logic of FuzzCreateContainerNoTearDown()
// and FuzzCreateContainerWithTearDown() and allows for
// the option to turn on/off teardown after each iteration.
// From a high level it:
// - Creates a client
// - Imports a bunch of fuzzed tar archives
// - Creates a bunch of containers
func doFuzz(data []byte, shouldTearDown bool) int {
	ctx, cancel := testContext(nil)
	defer cancel()

	// Check if daemon is running and start it if it isn't
	if ctrd.cmd == nil {
		startDaemon(ctx, shouldTearDown)
	}
	client, err := containerd.New(address)
	if err != nil {
		// The error here is most likely with the socket.
		// Deleting it will allow the creation of a new
		// socket during next fuzz iteration.
		deleteSocket()
		return -1
	}
	defer client.Close()
	f := fuzz.NewConsumer(data)

	// Begin import tars:
	noOfImports, err := f.GetInt()
	if err != nil {
		return 0
	}
	// maxImports is currently completely arbitrarily defined
	maxImports := 30
	for i := 0; i < noOfImports%maxImports; i++ {

		// f.TarBytes() returns valid tar bytes.
		tarBytes, err := f.TarBytes()
		if err != nil {
			return 0
		}
		_, _ = client.Import(ctx, bytes.NewReader(tarBytes))
	}
	// End import tars

	// Begin create containers:
	existingImages, err := client.ListImages(ctx)
	if err != nil {
		return 0
	}
	if len(existingImages) > 0 {
		noOfContainers, err := f.GetInt()
		if err != nil {
			return 0
		}
		// maxNoOfContainers is currently
		// completely arbitrarily defined
		maxNoOfContainers := 50
		for i := 0; i < noOfContainers%maxNoOfContainers; i++ {
			container, err := newContainer(client, f, ctx)
			if err == nil {
				defer container.Delete(ctx, containerd.WithSnapshotCleanup)
			}
		}
	}
	// End create containers

	return 1
}

// FuzzCreateContainerNoTearDown() implements a fuzzer
// similar to FuzzCreateContainerWithTearDown() with
// with one minor distinction: One tears down the
// daemon after each iteration whereas the other doesn't.
// The two fuzzers' performance will be compared over time.
func FuzzCreateContainerNoTearDown(data []byte) int {
	ret := doFuzz(data, false)
	return ret
}

// FuzzCreateContainerWithTearDown() is similar to
// FuzzCreateContainerNoTearDown() except that
// FuzzCreateContainerWithTearDown tears down the daemon
// after each iteration.
func FuzzCreateContainerWithTearDown(data []byte) int {
	ret := doFuzz(data, true)
	return ret
}
