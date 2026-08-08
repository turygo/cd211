package clouddrive

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const testHash = "0123456789abcdef0123456789abcdef01234567"

type fakeRPC struct {
	systemInfo    func(context.Context, *emptypb.Empty) (*pb.CloudDriveSystemInfo, error)
	getToken      func(context.Context, *pb.GetTokenRequest) (*pb.JWTToken, error)
	findFile      func(context.Context, *pb.FindFileByPathRequest) (*pb.CloudDriveFile, error)
	createFolder  func(context.Context, *pb.CreateFolderRequest) (*pb.CreateFolderResult, error)
	addOffline    func(context.Context, *pb.AddOfflineFileRequest) (*pb.FileOperationResult, error)
	removeOffline func(context.Context, *pb.RemoveOfflineFilesRequest) (*pb.FileOperationResult, error)
	listOffline   func(context.Context, *pb.FileRequest) (*pb.OfflineFileListResult, error)
	copyFile      func(context.Context, *pb.CopyFileRequest) (*pb.FileOperationResult, error)
	getCopy       func(context.Context, *emptypb.Empty) (*pb.GetCopyTaskResult, error)
	cancelCopy    func(context.Context, *pb.CopyTaskRequest) (*emptypb.Empty, error)
}

func (f *fakeRPC) GetSystemInfo(ctx context.Context, req *emptypb.Empty, _ ...grpc.CallOption) (*pb.CloudDriveSystemInfo, error) {
	return f.systemInfo(ctx, req)
}

func (f *fakeRPC) GetToken(ctx context.Context, req *pb.GetTokenRequest, _ ...grpc.CallOption) (*pb.JWTToken, error) {
	return f.getToken(ctx, req)
}

// FindFileByPath and CreateFolder default to "the offline folder is already
// there", so tests that predate the folder check keep their original meaning
// and only folder-specific tests have to wire these up.
func (f *fakeRPC) FindFileByPath(ctx context.Context, req *pb.FindFileByPathRequest, _ ...grpc.CallOption) (*pb.CloudDriveFile, error) {
	if f.findFile == nil {
		return &pb.CloudDriveFile{IsDirectory: true}, nil
	}
	return f.findFile(ctx, req)
}
func (f *fakeRPC) CreateFolder(ctx context.Context, req *pb.CreateFolderRequest, _ ...grpc.CallOption) (*pb.CreateFolderResult, error) {
	if f.createFolder == nil {
		return &pb.CreateFolderResult{Result: &pb.FileOperationResult{Success: true}}, nil
	}
	return f.createFolder(ctx, req)
}
func (f *fakeRPC) AddOfflineFiles(ctx context.Context, req *pb.AddOfflineFileRequest, _ ...grpc.CallOption) (*pb.FileOperationResult, error) {
	return f.addOffline(ctx, req)
}
func (f *fakeRPC) RemoveOfflineFiles(ctx context.Context, req *pb.RemoveOfflineFilesRequest, _ ...grpc.CallOption) (*pb.FileOperationResult, error) {
	return f.removeOffline(ctx, req)
}
func (f *fakeRPC) ListOfflineFilesByPath(ctx context.Context, req *pb.FileRequest, _ ...grpc.CallOption) (*pb.OfflineFileListResult, error) {
	return f.listOffline(ctx, req)
}
func (f *fakeRPC) CopyFile(ctx context.Context, req *pb.CopyFileRequest, _ ...grpc.CallOption) (*pb.FileOperationResult, error) {
	return f.copyFile(ctx, req)
}
func (f *fakeRPC) GetCopyTasks(ctx context.Context, req *emptypb.Empty, _ ...grpc.CallOption) (*pb.GetCopyTaskResult, error) {
	return f.getCopy(ctx, req)
}
func (f *fakeRPC) CancelCopyTask(ctx context.Context, req *pb.CopyTaskRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return f.cancelCopy(ctx, req)
}

type fakeCloser struct{ calls int }

func (c *fakeCloser) Close() error { c.calls++; return nil }

func newTestClient(t *testing.T, rpc *fakeRPC, now func() time.Time) *Client {
	t.Helper()
	client, err := New(rpc, &fakeCloser{}, " user ", "password", 30*time.Second, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func token(now time.Time) *pb.JWTToken {
	return &pb.JWTToken{Success: true, Token: "test-token", Expiration: timestamppb.New(now.Add(2 * time.Minute))}
}

func TestCheckCloudDriveAvailability(t *testing.T) {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	rpc := &fakeRPC{}
	rpc.systemInfo = func(ctx context.Context, _ *emptypb.Empty) (*pb.CloudDriveSystemInfo, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("system status request had no deadline")
		}
		return &pb.CloudDriveSystemInfo{IsLogin: true, SystemReady: true}, nil
	}
	client := newTestClient(t, rpc, func() time.Time { return base })
	if err := client.Check(context.Background()); err != nil {
		t.Fatalf("Check ready: %v", err)
	}

	rpc.systemInfo = func(context.Context, *emptypb.Empty) (*pb.CloudDriveSystemInfo, error) {
		hasError := true
		return &pb.CloudDriveSystemInfo{IsLogin: true, SystemReady: true, HasError: &hasError}, nil
	}
	assertErrorKind(t, client.Check(context.Background()), "system_status", ErrorTransient)

	rpc.systemInfo = func(context.Context, *emptypb.Empty) (*pb.CloudDriveSystemInfo, error) {
		return nil, status.Error(codes.Unavailable, "private upstream detail")
	}
	assertErrorKind(t, client.Check(context.Background()), "system_status", ErrorTransient)
}

func TestAuthenticationMetadataCacheBoundaryConcurrentAndTimeout(t *testing.T) {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	now := base
	var mu sync.Mutex
	calls := 0
	rpc := &fakeRPC{}
	rpc.getToken = func(ctx context.Context, req *pb.GetTokenRequest) (*pb.JWTToken, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		if req.UserName != "user" || req.Password != "password" {
			t.Errorf("credential request was changed")
		}
		if md, ok := metadata.FromOutgoingContext(ctx); !ok || len(md.Get("authorization")) != 0 {
			t.Errorf("token request carried authorization metadata: %v", md)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("token request had no deadline")
		}
		return token(now), nil
	}
	rpc.findFile = func(ctx context.Context, req *pb.FindFileByPathRequest) (*pb.CloudDriveFile, error) {
		if req.ParentPath != "/folder" || req.Path != "file" {
			t.Errorf("unexpected find request: %#v", req)
		}
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok || len(md) != 1 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
			t.Errorf("unexpected authorization metadata: %v", md)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("authorized request had no deadline")
		}
		return &pb.CloudDriveFile{FullPathName: "/folder/file"}, nil
	}
	client := newTestClient(t, rpc, func() time.Time { return now })
	parent := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer caller-token"))
	for range 2 {
		if _, err := client.FindFile(parent, "/folder/./file"); err != nil {
			t.Fatalf("FindFile: %v", err)
		}
	}
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("GetToken calls = %d, want 1", gotCalls)
	}
	now = base.Add(time.Minute)
	if _, err := client.FindFile(context.Background(), "/folder/file"); err != nil {
		t.Fatalf("refresh FindFile: %v", err)
	}
	mu.Lock()
	gotCalls = calls
	mu.Unlock()
	if gotCalls != 2 {
		t.Fatalf("GetToken calls at cache boundary = %d, want 2", gotCalls)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	calls = 0
	now = base
	rpc.getToken = func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
		return token(now), nil
	}
	concurrent := newTestClient(t, rpc, func() time.Time { return now })
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = concurrent.FindFile(context.Background(), "/folder/file") }()
	}
	<-entered
	close(release)
	wg.Wait()
	mu.Lock()
	gotCalls = calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("concurrent GetToken calls = %d, want 1", gotCalls)
	}
}

func TestOfflineRequestsMappingAndCrashAdoption(t *testing.T) {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	listCalls := 0
	rpc := &fakeRPC{}
	rpc.getToken = func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
	rpc.listOffline = func(_ context.Context, req *pb.FileRequest) (*pb.OfflineFileListResult, error) {
		if req.Path != "/cloud/folder" {
			t.Errorf("list path = %q", req.Path)
		}
		listCalls++
		if listCalls == 1 {
			return &pb.OfflineFileListResult{OfflineFiles: []*pb.OfflineFile{{InfoHash: "bad"}}}, nil
		}
		return &pb.OfflineFileListResult{OfflineFiles: []*pb.OfflineFile{{Name: "movie", InfoHash: "0123456789ABCDEF0123456789ABCDEF01234567", Status: pb.OfflineFileStatus_OFFLINE_DOWNLOADING, PercendDone: 35}}}, nil
	}
	rpc.addOffline = func(_ context.Context, req *pb.AddOfflineFileRequest) (*pb.FileOperationResult, error) {
		if req.Urls != "magnet:?xt=urn:btih:private" || req.ToFolder != "/cloud/folder" {
			t.Errorf("unexpected add request: %#v", req)
		}
		return nil, status.Error(codes.AlreadyExists, "must not leak URI")
	}
	client := newTestClient(t, rpc, func() time.Time { return base })
	task, err := client.EnsureOffline(context.Background(), OfflineSpec{SubmissionURI: "magnet:?xt=urn:btih:private", CloudFolder: "/cloud//folder", Hash: testHash})
	if err != nil {
		t.Fatalf("EnsureOffline crash adoption: %v", err)
	}
	if task != (OfflineTask{Name: "movie", InfoHash: testHash, SourcePath: "/cloud/folder/movie", State: OfflineDownloading, Progress: .35}) {
		t.Fatalf("task = %#v", task)
	}

	delayedRPC := &fakeRPC{}
	delayedRPC.getToken = func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
	delayedRPC.listOffline = func(context.Context, *pb.FileRequest) (*pb.OfflineFileListResult, error) {
		return &pb.OfflineFileListResult{}, nil
	}
	delayedRPC.addOffline = func(context.Context, *pb.AddOfflineFileRequest) (*pb.FileOperationResult, error) {
		return nil, status.Error(codes.AlreadyExists, "not visible yet")
	}
	delayedClient := newTestClient(t, delayedRPC, func() time.Time { return base })
	delayedTask, err := delayedClient.EnsureOffline(context.Background(), OfflineSpec{
		SubmissionURI: "magnet:?xt=urn:btih:private", CloudFolder: "/cloud/folder", Hash: testHash,
	})
	if err != nil || delayedTask != (OfflineTask{InfoHash: testHash, State: OfflineInit}) {
		t.Fatalf("EnsureOffline delayed visibility = (%+v, %v)", delayedTask, err)
	}

	var cancelReq *pb.RemoveOfflineFilesRequest
	rpc.removeOffline = func(_ context.Context, req *pb.RemoveOfflineFilesRequest) (*pb.FileOperationResult, error) {
		cancelReq = req
		return &pb.FileOperationResult{Success: true}, nil
	}
	if err := client.CancelOffline(context.Background(), "/cloud/folder", testHash); err != nil {
		t.Fatalf("CancelOffline: %v", err)
	}
	if cancelReq.GetPath() != "/cloud/folder" || cancelReq.DeleteFiles || len(cancelReq.InfoHashes) != 1 || cancelReq.InfoHashes[0] != testHash || cancelReq.CloudName != "" || cancelReq.CloudAccountId != "" {
		t.Fatalf("unsafe cancel request: %#v", cancelReq)
	}

	rpc.removeOffline = func(context.Context, *pb.RemoveOfflineFilesRequest) (*pb.FileOperationResult, error) {
		return &pb.FileOperationResult{Success: false}, nil
	}
	rpc.listOffline = func(context.Context, *pb.FileRequest) (*pb.OfflineFileListResult, error) {
		return &pb.OfflineFileListResult{}, nil
	}
	if err := client.CancelOffline(context.Background(), "/cloud/folder", testHash); err != nil {
		t.Fatalf("idempotent CancelOffline: %v", err)
	}
}

func TestOfflineInspectionRejectsFallbackAndInvalidCandidate(t *testing.T) {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	rpc := &fakeRPC{}
	rpc.getToken = func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
	rpc.listOffline = func(_ context.Context, _ *pb.FileRequest) (*pb.OfflineFileListResult, error) {
		return &pb.OfflineFileListResult{OfflineFiles: []*pb.OfflineFile{{Name: "same-name", InfoHash: "not-a-v1-hash"}}}, nil
	}
	client := newTestClient(t, rpc, func() time.Time { return base })
	if _, found, err := client.InspectOffline(context.Background(), "/cloud", testHash); err != nil || found {
		t.Fatalf("name fallback: found=%v err=%v", found, err)
	}
	rpc.listOffline = func(_ context.Context, _ *pb.FileRequest) (*pb.OfflineFileListResult, error) {
		return &pb.OfflineFileListResult{OfflineFiles: []*pb.OfflineFile{{Name: "bad/name", InfoHash: testHash, Status: pb.OfflineFileStatus_OFFLINE_UNKNOWN}}}, nil
	}
	_, _, err := client.InspectOffline(context.Background(), "/cloud", testHash)
	assertErrorKind(t, err, "list_offline", ErrorInvalidResponse)
}

func TestEnsureOfflineCreatesMissingCloudFolder(t *testing.T) {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)

	newFolderRPC := func() (*fakeRPC, *int, *int, *bool) {
		created, adds := 0, 0
		exists := false
		rpc := &fakeRPC{}
		rpc.getToken = func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
		rpc.findFile = func(_ context.Context, req *pb.FindFileByPathRequest) (*pb.CloudDriveFile, error) {
			if req.ParentPath != "/cloud" || req.Path != "folder" {
				return nil, status.Error(codes.InvalidArgument, "unexpected find")
			}
			if !exists {
				return nil, status.Error(codes.NotFound, "missing")
			}
			return &pb.CloudDriveFile{Name: "folder", FullPathName: "/cloud/folder", IsDirectory: true}, nil
		}
		rpc.createFolder = func(_ context.Context, req *pb.CreateFolderRequest) (*pb.CreateFolderResult, error) {
			if req.ParentPath != "/cloud" || req.FolderName != "folder" {
				t.Errorf("unexpected create request: %#v", req)
			}
			created++
			exists = true
			return &pb.CreateFolderResult{Result: &pb.FileOperationResult{Success: true}}, nil
		}
		rpc.listOffline = func(_ context.Context, _ *pb.FileRequest) (*pb.OfflineFileListResult, error) {
			return &pb.OfflineFileListResult{}, nil
		}
		rpc.addOffline = func(_ context.Context, _ *pb.AddOfflineFileRequest) (*pb.FileOperationResult, error) {
			adds++
			return &pb.FileOperationResult{Success: true}, nil
		}
		return rpc, &created, &adds, &exists
	}

	spec := OfflineSpec{SubmissionURI: "magnet:?xt=urn:btih:x", CloudFolder: "/cloud//folder", Hash: testHash}

	t.Run("missing folder is created before submitting", func(t *testing.T) {
		rpc, created, adds, _ := newFolderRPC()
		client := newTestClient(t, rpc, func() time.Time { return base })
		task, err := client.EnsureOffline(context.Background(), spec)
		if err != nil {
			t.Fatalf("EnsureOffline: %v", err)
		}
		if task != (OfflineTask{InfoHash: testHash, State: OfflineInit}) {
			t.Fatalf("task = %#v", task)
		}
		if *created != 1 || *adds != 1 {
			t.Fatalf("created = %d, adds = %d, want 1 and 1", *created, *adds)
		}
	})

	t.Run("existing folder is not recreated", func(t *testing.T) {
		rpc, created, adds, exists := newFolderRPC()
		*exists = true
		client := newTestClient(t, rpc, func() time.Time { return base })
		if _, err := client.EnsureOffline(context.Background(), spec); err != nil {
			t.Fatalf("EnsureOffline: %v", err)
		}
		if *created != 0 || *adds != 1 {
			t.Fatalf("created = %d, adds = %d, want 0 and 1", *created, *adds)
		}
	})

	t.Run("a file where the folder belongs is permanent", func(t *testing.T) {
		rpc, created, adds, _ := newFolderRPC()
		rpc.findFile = func(_ context.Context, _ *pb.FindFileByPathRequest) (*pb.CloudDriveFile, error) {
			return &pb.CloudDriveFile{Name: "folder", FullPathName: "/cloud/folder"}, nil
		}
		client := newTestClient(t, rpc, func() time.Time { return base })
		_, err := client.EnsureOffline(context.Background(), spec)
		assertErrorKind(t, err, "create_folder", ErrorPermanent)
		if *created != 0 || *adds != 0 {
			t.Fatalf("created = %d, adds = %d, want 0 and 0", *created, *adds)
		}
	})

	t.Run("a folder that appears after a failed create is adopted", func(t *testing.T) {
		rpc, _, adds, exists := newFolderRPC()
		rpc.createFolder = func(_ context.Context, _ *pb.CreateFolderRequest) (*pb.CreateFolderResult, error) {
			*exists = true // a concurrent reconciler won the race
			return nil, status.Error(codes.AlreadyExists, "exists")
		}
		client := newTestClient(t, rpc, func() time.Time { return base })
		if _, err := client.EnsureOffline(context.Background(), spec); err != nil {
			t.Fatalf("EnsureOffline: %v", err)
		}
		if *adds != 1 {
			t.Fatalf("adds = %d, want 1", *adds)
		}
	})

	t.Run("a failed create that leaves nothing behind stops the submission", func(t *testing.T) {
		rpc, _, adds, _ := newFolderRPC()
		rpc.createFolder = func(_ context.Context, _ *pb.CreateFolderRequest) (*pb.CreateFolderResult, error) {
			return &pb.CreateFolderResult{Result: &pb.FileOperationResult{Success: false}}, nil
		}
		client := newTestClient(t, rpc, func() time.Time { return base })
		_, err := client.EnsureOffline(context.Background(), spec)
		assertErrorKind(t, err, "create_folder", ErrorPermanent)
		if *adds != 0 {
			t.Fatalf("adds = %d, want 0", *adds)
		}
	})
}

func TestCopyRequestsMappingAndCrashAdoption(t *testing.T) {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	getCalls := 0
	rpc := &fakeRPC{}
	rpc.getToken = func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
	rpc.getCopy = func(_ context.Context, _ *emptypb.Empty) (*pb.GetCopyTaskResult, error) {
		getCalls++
		if getCalls == 1 {
			return &pb.GetCopyTaskResult{}, nil
		}
		return &pb.GetCopyTaskResult{CopyTasks: []*pb.CopyTask{{SourcePath: "/src/item", DestPath: "/dst", Status: pb.CopyTask_Scanned, TotalBytes: 10, UploadedBytes: 5, Errors: []*pb.TaskError{{}}}}}, nil
	}
	rpc.copyFile = func(_ context.Context, req *pb.CopyFileRequest) (*pb.FileOperationResult, error) {
		if len(req.TheFilePaths) != 1 || req.TheFilePaths[0] != "/src/item" || req.DestPath != "/dst" || req.ConflictPolicy == nil || *req.ConflictPolicy != pb.CopyFileRequest_Skip || req.HandleConflictRecursively == nil || *req.HandleConflictRecursively {
			t.Errorf("unexpected copy request: %#v", req)
		}
		return nil, status.Error(codes.AlreadyExists, "already adopted")
	}
	client := newTestClient(t, rpc, func() time.Time { return base })
	task, err := client.EnsureCopy(context.Background(), CopySpec{SourcePath: "/src//item", DestinationPath: "/dst"})
	if err != nil {
		t.Fatalf("EnsureCopy crash adoption: %v", err)
	}
	if task != (CopyTask{SourcePath: "/src/item", DestinationPath: "/dst", State: CopyScanned, Progress: .5, ErrorCount: 1}) {
		t.Fatalf("task = %#v", task)
	}

	delayedRPC := &fakeRPC{}
	delayedRPC.getToken = func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
	delayedRPC.getCopy = func(context.Context, *emptypb.Empty) (*pb.GetCopyTaskResult, error) {
		return &pb.GetCopyTaskResult{}, nil
	}
	delayedRPC.copyFile = func(context.Context, *pb.CopyFileRequest) (*pb.FileOperationResult, error) {
		return nil, status.Error(codes.AlreadyExists, "not visible yet")
	}
	delayedClient := newTestClient(t, delayedRPC, func() time.Time { return base })
	delayedTask, err := delayedClient.EnsureCopy(context.Background(), CopySpec{SourcePath: "/src/item", DestinationPath: "/dst"})
	if err != nil || delayedTask != (CopyTask{SourcePath: "/src/item", DestinationPath: "/dst", State: CopyPending}) {
		t.Fatalf("EnsureCopy delayed visibility = (%+v, %v)", delayedTask, err)
	}

	var cancelReq *pb.CopyTaskRequest
	rpc.cancelCopy = func(_ context.Context, req *pb.CopyTaskRequest) (*emptypb.Empty, error) {
		cancelReq = req
		return &emptypb.Empty{}, nil
	}
	if err := client.CancelCopy(context.Background(), "/src/item", "/dst"); err != nil {
		t.Fatalf("CancelCopy: %v", err)
	}
	if cancelReq.SourcePath != "/src/item" || cancelReq.DestPath != "/dst" {
		t.Fatalf("cancel request = %#v", cancelReq)
	}

	rpc.cancelCopy = func(context.Context, *pb.CopyTaskRequest) (*emptypb.Empty, error) {
		return nil, status.Error(codes.NotFound, "already removed")
	}
	if err := client.CancelCopy(context.Background(), "/src/item", "/dst"); err != nil {
		t.Fatalf("idempotent CancelCopy: %v", err)
	}
}

func TestCopyInspectionValidationAndStateProgressMappings(t *testing.T) {
	if got, err := mapOffline("/cloud", testHash, &pb.OfflineFile{Name: "x", Status: pb.OfflineFileStatus_OFFLINE_FINISHED, PercendDone: 1}); err != nil || got.State != OfflineFinished || got.Progress != 1 {
		t.Fatalf("offline mapping: %#v, %v", got, err)
	}
	if _, err := mapOffline("/cloud", testHash, &pb.OfflineFile{Name: "x", Status: pb.OfflineFileStatus_OFFLINE_INIT, PercendDone: 101}); err == nil {
		t.Fatal("accepted out-of-range offline progress")
	}
	states := []pb.CopyTask_TaskStatus{pb.CopyTask_Pending, pb.CopyTask_Scanning, pb.CopyTask_Scanned, pb.CopyTask_Completed, pb.CopyTask_Failed}
	for _, state := range states {
		if _, ok := copyState(state); !ok {
			t.Fatalf("unmapped copy state %v", state)
		}
	}
	if got, err := mapCopy("/s", "/d", &pb.CopyTask{Status: pb.CopyTask_Completed}); err != nil || got.Progress != 1 {
		t.Fatalf("zero-byte completed mapping: %#v, %v", got, err)
	}
	if _, err := mapCopy("/s", "/d", &pb.CopyTask{Status: pb.CopyTask_Pending, TotalBytes: 1, UploadedBytes: 2}); err == nil {
		t.Fatal("accepted invalid copy progress")
	}

	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	rpc := &fakeRPC{}
	rpc.getToken = func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
	rpc.getCopy = func(_ context.Context, _ *emptypb.Empty) (*pb.GetCopyTaskResult, error) {
		return &pb.GetCopyTaskResult{CopyTasks: []*pb.CopyTask{{SourcePath: "/other", DestPath: "/d", Status: pb.CopyTask_Pending}, {SourcePath: "/s", DestPath: "/d", Status: pb.CopyTask_Pending, Errors: []*pb.TaskError{nil}}}}, nil
	}
	client := newTestClient(t, rpc, func() time.Time { return base })
	_, _, err := client.InspectCopy(context.Background(), "/s", "/d")
	assertErrorKind(t, err, "list_copy", ErrorInvalidResponse)
}

func TestSafeErrorsNilResponsesStatusClassificationAndClose(t *testing.T) {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	closer := &fakeCloser{}
	rpc := &fakeRPC{}
	rpc.getToken = func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
	rpc.findFile = func(_ context.Context, _ *pb.FindFileByPathRequest) (*pb.CloudDriveFile, error) {
		return nil, status.Error(codes.Unavailable, "private request")
	}
	client, err := New(rpc, closer, "user", "password", time.Second, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.FindFile(context.Background(), "/file")
	assertErrorKind(t, err, "find_file", ErrorTransient)
	if errors.Is(err, errors.New("private request")) {
		t.Fatal("unexpected unrelated error match")
	}
	if err.Error() != "clouddrive find_file: transient" {
		t.Fatalf("unsafe error: %q", err)
	}
	rpc.findFile = func(_ context.Context, _ *pb.FindFileByPathRequest) (*pb.CloudDriveFile, error) { return nil, nil }
	_, err = client.FindFile(context.Background(), "/file")
	assertErrorKind(t, err, "find_file", ErrorInvalidResponse)
	rpc.findFile = func(_ context.Context, _ *pb.FindFileByPathRequest) (*pb.CloudDriveFile, error) {
		return nil, status.Error(codes.InvalidArgument, "bad request")
	}
	_, err = client.FindFile(context.Background(), "/file")
	assertErrorKind(t, err, "find_file", ErrorPermanent)
	rpc.findFile = func(_ context.Context, _ *pb.FindFileByPathRequest) (*pb.CloudDriveFile, error) {
		return nil, status.Error(codes.PermissionDenied, "forbidden")
	}
	_, err = client.FindFile(context.Background(), "/file")
	assertErrorKind(t, err, "find_file", ErrorUnauthorized)
	rpc.removeOffline = func(_ context.Context, _ *pb.RemoveOfflineFilesRequest) (*pb.FileOperationResult, error) {
		return &pb.FileOperationResult{}, nil
	}
	rpc.listOffline = func(context.Context, *pb.FileRequest) (*pb.OfflineFileListResult, error) {
		return &pb.OfflineFileListResult{OfflineFiles: []*pb.OfflineFile{{
			Name: "failed", InfoHash: testHash, Status: pb.OfflineFileStatus_OFFLINE_ERROR,
		}}}, nil
	}
	err = client.CancelOffline(context.Background(), "/folder", testHash)
	assertErrorKind(t, err, "cancel_offline", ErrorPermanent)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if closer.calls != 1 {
		t.Fatalf("Close calls = %d", closer.calls)
	}
	if _, err := New(nil, closer, "user", "password", time.Second, func() time.Time { return base }); err == nil {
		t.Fatal("accepted nil rpc")
	}
	rejected := &fakeRPC{getToken: func(_ context.Context, _ *pb.GetTokenRequest) (*pb.JWTToken, error) {
		return &pb.JWTToken{Success: false}, nil
	}}
	_, err = newTestClient(t, rejected, func() time.Time { return base }).FindFile(context.Background(), "/file")
	assertErrorKind(t, err, "authenticate", ErrorUnauthorized)
}

func assertErrorKind(t *testing.T, err error, operation string, kind ErrorKind) {
	t.Helper()
	var cloudErr *Error
	if !errors.As(err, &cloudErr) || cloudErr.Operation != operation || cloudErr.Kind != kind {
		t.Fatalf("error = %v, want %s/%s", err, operation, kind)
	}
}
