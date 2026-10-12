package clouddrive

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive/pb"
)

// TestLiveCopyLifecycle 只操作测试自行创建的目录；必须在与 CloudDrive2 路径一致的容器中运行。
func TestLiveCopyLifecycle(t *testing.T) {
	address := os.Getenv("CD211_LIVE_ADDRESS")
	if address == "" {
		t.Skip("set CD211_LIVE_ADDRESS, CD211_LIVE_USERNAME, CD211_LIVE_PASSWORD, CD211_LIVE_ROOT and CD211_LIVE_CLOUD_ROOT to run")
	}
	root := os.Getenv("CD211_LIVE_ROOT")
	if !filepath.IsAbs(root) {
		t.Fatal("CD211_LIVE_ROOT must be an existing absolute shared directory")
	}
	cloudRoot := os.Getenv("CD211_LIVE_CLOUD_ROOT")
	if !path.IsAbs(cloudRoot) || path.Clean(cloudRoot) == "/" {
		t.Fatal("CD211_LIVE_CLOUD_ROOT must be an existing cloud directory below the root")
	}
	client, err := Dial(address, os.Getenv("CD211_LIVE_USERNAME"), os.Getenv("CD211_LIVE_PASSWORD"), 10*time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := client.Authenticate(ctx); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "cd211-contract-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	cloudFolder := path.Join(cloudRoot, filepath.Base(dir))
	if _, err := client.CreateDirectory(ctx, cloudRoot, filepath.Base(dir)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("CD211 isolated copy lifecycle contract\n")
	specs := make([]CopySpec, 0, 2)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, spec := range specs {
			if err := client.RemoveCopy(cleanupCtx, spec.SourcePath, spec.DestinationPath); err != nil {
				t.Errorf("cleanup copy: %v; retained fixture %s", err, dir)
				return
			}
			if _, found, err := client.InspectCopy(cleanupCtx, spec.SourcePath, spec.DestinationPath); err != nil || found {
				t.Errorf("cleanup not confirmed: found=%t error=%v; retained fixture %s", found, err, dir)
				return
			}
		}
		callCtx, callCancel, err := client.authorizedContext(cleanupCtx)
		if err != nil {
			t.Error(err)
			return
		}
		result, deleteErr := client.rpc.(pb.CloudDriveFileSrvClient).DeleteFile(callCtx, &pb.FileRequest{Path: cloudFolder})
		callCancel()
		if deleteErr != nil || result == nil || !result.Success {
			t.Errorf("cleanup cloud fixture: %v", deleteErr)
			return
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"primary", "sentinel"} {
		source := path.Join(cloudFolder, name)
		if _, err := client.CreateDirectory(ctx, cloudFolder, name); err != nil {
			t.Fatal(err)
		}
		liveUploadFixture(t, ctx, client, source, payload)
		destination := filepath.Join(dir, name+"-output")
		if err := os.Mkdir(destination, 0o770); err != nil {
			t.Fatal(err)
		}
		specs = append(specs, CopySpec{SourcePath: source, DestinationPath: destination})
	}
	for _, spec := range specs {
		liveCopyAndVerify(t, ctx, client, spec, payload)
	}
	primary := specs[0]
	if err := client.RemoveCopy(ctx, primary.SourcePath, primary.DestinationPath); err != nil {
		t.Fatal(err)
	}
	if _, found, err := client.InspectCopy(ctx, primary.SourcePath, primary.DestinationPath); err != nil || found {
		t.Fatalf("completed task removal: found=%t error=%v", found, err)
	}
	if err := client.RemoveCopy(ctx, primary.SourcePath, primary.DestinationPath); err != nil {
		t.Fatalf("idempotent removal: %v", err)
	}
	if sentinel, found, err := client.InspectCopy(ctx, specs[1].SourcePath, specs[1].DestinationPath); err != nil || !found || sentinel.State != CopyCompleted {
		t.Fatalf("unrelated task changed: task=%+v found=%t error=%v", sentinel, found, err)
	}
	if file, err := client.FindFile(ctx, path.Join(primary.SourcePath, "payload.bin")); err != nil || file.GetSize() != int64(len(payload)) {
		t.Fatalf("source changed: error=%v", err)
	}
	if err := os.RemoveAll(primary.DestinationPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(primary.DestinationPath, 0o770); err != nil {
		t.Fatal(err)
	}
	liveCopyAndVerify(t, ctx, client, primary, payload)
}

func liveCopyAndVerify(t *testing.T, ctx context.Context, client *Client, spec CopySpec, payload []byte) {
	t.Helper()
	if _, err := client.EnsureCopy(ctx, spec); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(40 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, found, err := client.InspectCopy(ctx, spec.SourcePath, spec.DestinationPath)
		if err != nil {
			t.Fatal(err)
		}
		if found && task.State == CopyFailed {
			t.Fatal("fixture copy failed")
		}
		if found && task.State == CopyCompleted {
			data, readErr := os.ReadFile(filepath.Join(spec.DestinationPath, filepath.Base(spec.SourcePath), "payload.bin"))
			if readErr == nil && bytes.Equal(data, payload) {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("fixture copy did not produce expected bytes: task=%+v found=%t", task, found)
		case <-ticker.C:
		}
	}
}

func liveUploadFixture(t *testing.T, ctx context.Context, client *Client, folder string, payload []byte) {
	t.Helper()
	rpc := client.rpc.(pb.CloudDriveFileSrvClient)
	callCtx, cancel, err := client.authorizedContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	file, err := rpc.CreateFile(callCtx, &pb.CreateFileRequest{ParentPath: folder, FileName: "payload.bin"})
	if err != nil {
		t.Fatal(err)
	}
	written, err := rpc.WriteToFile(callCtx, &pb.WriteFileRequest{FileHandle: file.FileHandle, Length: uint64(len(payload)), Buffer: payload, CloseFile: true})
	if err != nil || written == nil || written.BytesWritten != uint64(len(payload)) {
		t.Fatalf("upload fixture: %v", err)
	}
	deadline := time.NewTimer(40 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		refreshCtx, refreshCancel, authErr := client.authorizedContext(ctx)
		if authErr != nil {
			t.Fatal(authErr)
		}
		stream, listErr := rpc.GetSubFiles(refreshCtx, &pb.ListSubFileRequest{Path: folder, ForceRefresh: true})
		found := false
		if listErr == nil {
			for {
				reply, recvErr := stream.Recv()
				if recvErr == io.EOF {
					break
				}
				if recvErr != nil {
					listErr = recvErr
					break
				}
				for _, item := range reply.SubFiles {
					if item.Name == "payload.bin" && item.Size == int64(len(payload)) {
						found = true
					}
				}
			}
		}
		refreshCancel()
		if listErr != nil {
			t.Fatal(listErr)
		}
		if found {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("cloud fixture upload did not become visible")
		case <-ticker.C:
		}
	}
}
