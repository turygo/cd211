package clouddrive

import (
	"context"
	"errors"
	"net"
	"path"
	"slices"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const contractToken = "contract-token"

type grpcContractServer struct {
	pb.UnimplementedCloudDriveFileSrvServer

	base time.Time

	statusCalls        int
	tokenCalls         int
	listDirectoryCalls int
	findCalls          int
	createFolderCalls  int
	addOfflineCalls    int
	removeOfflineCalls int
	offlineListCalls   int
	copyFileCalls      int
	copyListCalls      int
	cancelCopyCalls    int
	offlineAdded       bool
	folderCreated      bool
	copyAdded          bool
}

func (s *grpcContractServer) GetSystemInfo(ctx context.Context, _ *emptypb.Empty) (*pb.CloudDriveSystemInfo, error) {
	if authorizationValues(ctx) != nil {
		return nil, status.Error(codes.InvalidArgument, "system status included authorization")
	}
	s.statusCalls++
	return &pb.CloudDriveSystemInfo{IsLogin: true, SystemReady: true}, nil
}

func (s *grpcContractServer) GetToken(ctx context.Context, req *pb.GetTokenRequest) (*pb.JWTToken, error) {
	if req.GetUserName() != "contract-user" || req.GetPassword() != "contract-password" || req.TotpCode != nil {
		return nil, status.Error(codes.InvalidArgument, "unexpected token request")
	}
	if authorizationValues(ctx) != nil {
		return nil, status.Error(codes.InvalidArgument, "token request included authorization")
	}
	s.tokenCalls++
	return &pb.JWTToken{
		Success:    true,
		Token:      contractToken,
		Expiration: timestamppb.New(s.base.Add(10 * time.Minute)),
	}, nil
}

func (s *grpcContractServer) GetSubFiles(req *pb.ListSubFileRequest, stream grpc.ServerStreamingServer[pb.SubFilesReply]) error {
	if err := requireContractAuthorization(stream.Context()); err != nil {
		return err
	}
	if req.GetPath() != "/cloud" || req.GetForceRefresh() || req.CheckExpires != nil {
		return status.Error(codes.InvalidArgument, "unexpected directory list request")
	}
	s.listDirectoryCalls++
	if err := stream.Send(&pb.SubFilesReply{SubFiles: []*pb.CloudDriveFile{
		{Name: "Zeta", FullPathName: "/cloud/Zeta", IsDirectory: true},
		{Name: "movie.mkv", FullPathName: "/cloud/movie.mkv"},
	}}); err != nil {
		return err
	}
	return stream.Send(&pb.SubFilesReply{SubFiles: []*pb.CloudDriveFile{
		{Name: "alpha", FullPathName: "/cloud/alpha", IsDirectory: true},
	}})
}

func (s *grpcContractServer) FindFileByPath(ctx context.Context, req *pb.FindFileByPathRequest) (*pb.CloudDriveFile, error) {
	if err := requireContractAuthorization(ctx); err != nil {
		return nil, err
	}
	switch {
	case req.ParentPath == "/cloud/folder" && req.Path == "movie":
		s.findCalls++
		return &pb.CloudDriveFile{Name: "movie", FullPathName: "/cloud/folder/movie"}, nil
	case req.ParentPath == "/cloud" && req.Path == "folder":
		// The offline target folder does not exist until CreateFolder runs.
		if !s.folderCreated {
			return nil, status.Error(codes.NotFound, "folder not found")
		}
		return &pb.CloudDriveFile{Name: "folder", FullPathName: "/cloud/folder", IsDirectory: true}, nil
	case req.ParentPath == "/cloud" && req.Path == "New":
		// The CreateDirectory leaf does not exist until CreateFolder runs; a
		// verified not-found lookup is what authorizes creating it.
		return nil, status.Error(codes.NotFound, "folder not found")
	default:
		return nil, status.Error(codes.InvalidArgument, "unexpected find request")
	}
}

func (s *grpcContractServer) CreateFolder(ctx context.Context, req *pb.CreateFolderRequest) (*pb.CreateFolderResult, error) {
	if err := requireContractAuthorization(ctx); err != nil {
		return nil, err
	}
	switch {
	case req.ParentPath == "/cloud" && req.FolderName == "folder":
		s.folderCreated = true
	case req.ParentPath == "/cloud" && req.FolderName == "New":
	default:
		return nil, status.Error(codes.InvalidArgument, "unexpected create folder request")
	}
	s.createFolderCalls++
	return &pb.CreateFolderResult{
		FolderCreated: &pb.CloudDriveFile{Name: req.FolderName, FullPathName: path.Join(req.ParentPath, req.FolderName), IsDirectory: true},
		Result:        &pb.FileOperationResult{Success: true},
	}, nil
}

func (s *grpcContractServer) AddOfflineFiles(ctx context.Context, req *pb.AddOfflineFileRequest) (*pb.FileOperationResult, error) {
	if err := requireContractAuthorization(ctx); err != nil {
		return nil, err
	}
	if req.Urls != "magnet:?xt=urn:btih:contract" || req.ToFolder != "/cloud/folder" || req.CheckFolderAfterSecs != nil {
		return nil, status.Error(codes.InvalidArgument, "unexpected add offline request")
	}
	s.addOfflineCalls++
	s.offlineAdded = true
	return &pb.FileOperationResult{Success: true}, nil
}

func (s *grpcContractServer) RemoveOfflineFiles(ctx context.Context, req *pb.RemoveOfflineFilesRequest) (*pb.FileOperationResult, error) {
	if err := requireContractAuthorization(ctx); err != nil {
		return nil, err
	}
	if req.CloudName != "" || req.CloudAccountId != "" || req.DeleteFiles || req.Path == nil || *req.Path != "/cloud/folder" || len(req.InfoHashes) != 1 || req.InfoHashes[0] != testHash {
		return nil, status.Error(codes.InvalidArgument, "unexpected remove offline request")
	}
	s.removeOfflineCalls++
	return &pb.FileOperationResult{Success: true}, nil
}

func (s *grpcContractServer) ListOfflineFilesByPath(ctx context.Context, req *pb.FileRequest) (*pb.OfflineFileListResult, error) {
	if err := requireContractAuthorization(ctx); err != nil {
		return nil, err
	}
	if req.Path != "/cloud/folder" || req.ForceRefresh != nil {
		return nil, status.Error(codes.InvalidArgument, "unexpected list offline request")
	}
	s.offlineListCalls++
	if !s.offlineAdded {
		return &pb.OfflineFileListResult{}, nil
	}
	return &pb.OfflineFileListResult{OfflineFiles: []*pb.OfflineFile{{
		Name:        "movie",
		InfoHash:    testHash,
		Status:      pb.OfflineFileStatus_OFFLINE_DOWNLOADING,
		PercendDone: 35,
	}}}, nil
}

func (s *grpcContractServer) CopyFile(ctx context.Context, req *pb.CopyFileRequest) (*pb.FileOperationResult, error) {
	if err := requireContractAuthorization(ctx); err != nil {
		return nil, err
	}
	if len(req.TheFilePaths) != 1 || req.TheFilePaths[0] != "/cloud/source" || req.DestPath != "/cloud/destination" || req.ConflictPolicy == nil || *req.ConflictPolicy != pb.CopyFileRequest_Skip || req.HandleConflictRecursively == nil || *req.HandleConflictRecursively {
		return nil, status.Error(codes.InvalidArgument, "unexpected copy request")
	}
	s.copyFileCalls++
	s.copyAdded = true
	return &pb.FileOperationResult{Success: true}, nil
}

func (s *grpcContractServer) GetCopyTasks(ctx context.Context, _ *emptypb.Empty) (*pb.GetCopyTaskResult, error) {
	if err := requireContractAuthorization(ctx); err != nil {
		return nil, err
	}
	s.copyListCalls++
	if !s.copyAdded {
		return &pb.GetCopyTaskResult{}, nil
	}
	return &pb.GetCopyTaskResult{CopyTasks: []*pb.CopyTask{{
		SourcePath:    "/cloud/source",
		DestPath:      "/cloud/destination",
		Status:        pb.CopyTask_Scanning,
		TotalBytes:    200,
		UploadedBytes: 70,
	}}}, nil
}

func (s *grpcContractServer) CancelCopyTask(ctx context.Context, req *pb.CopyTaskRequest) (*emptypb.Empty, error) {
	if err := requireContractAuthorization(ctx); err != nil {
		return nil, err
	}
	if req.SourcePath != "/cloud/source" || req.DestPath != "/cloud/destination" {
		return nil, status.Error(codes.InvalidArgument, "unexpected cancel copy request")
	}
	s.cancelCopyCalls++
	return &emptypb.Empty{}, nil
}

func requireContractAuthorization(ctx context.Context) error {
	values := authorizationValues(ctx)
	if len(values) != 1 || values[0] != "Bearer "+contractToken {
		return status.Error(codes.Unauthenticated, "missing contract authorization")
	}
	return nil
}

func authorizationValues(ctx context.Context) []string {
	metadata, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	return metadata.Get("authorization")
}

func TestClientGRPCTransportContract(t *testing.T) {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	contract := &grpcContractServer{base: base}
	pb.RegisterCloudDriveFileSrvServer(server, contract)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		if err := <-serveErr; err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("bufconn server: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		"bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	client, err := New(pb.NewCloudDriveFileSrvClient(conn), conn, "contract-user", "contract-password", time.Second, func() time.Time { return base })
	if err != nil {
		_ = conn.Close()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close client connection: %v", err)
		}
	})

	if err := client.Check(ctx); err != nil {
		t.Fatalf("Check: %v", err)
	}
	file, err := client.FindFile(ctx, "/cloud//folder/movie")
	if err != nil {
		t.Fatalf("FindFile: %v", err)
	}
	if file.Name != "movie" || file.FullPathName != "/cloud/folder/movie" {
		t.Fatalf("FindFile response = %#v", file)
	}

	directories, err := client.ListDirectories(ctx, "/cloud")
	if err != nil {
		t.Fatalf("ListDirectories: %v", err)
	}
	wantDirectories := []Directory{{Name: "alpha", Path: "/cloud/alpha"}, {Name: "Zeta", Path: "/cloud/Zeta"}}
	if !slices.Equal(directories, wantDirectories) {
		t.Fatalf("ListDirectories = %#v, want %#v", directories, wantDirectories)
	}
	created, err := client.CreateDirectory(ctx, "/cloud", "New")
	if err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}
	if created != (Directory{Name: "New", Path: "/cloud/New"}) {
		t.Fatalf("CreateDirectory = %#v", created)
	}

	offline, err := client.EnsureOffline(ctx, OfflineSpec{
		SubmissionURI: "magnet:?xt=urn:btih:contract",
		CloudFolder:   "/cloud//folder",
		Hash:          testHash,
	})
	if err != nil {
		t.Fatalf("EnsureOffline: %v", err)
	}
	wantOffline := OfflineTask{Name: "movie", InfoHash: testHash, SourcePath: "/cloud/folder/movie", State: OfflineDownloading, Progress: .35}
	if offline != wantOffline {
		t.Fatalf("EnsureOffline task = %#v, want %#v", offline, wantOffline)
	}
	inspectedOffline, found, err := client.InspectOffline(ctx, "/cloud//folder", testHash)
	if err != nil || !found || inspectedOffline != wantOffline {
		t.Fatalf("InspectOffline = (%#v, %t, %v), want (%#v, true, nil)", inspectedOffline, found, err, wantOffline)
	}
	if err := client.CancelOffline(ctx, "/cloud//folder", testHash); err != nil {
		t.Fatalf("CancelOffline: %v", err)
	}

	copyTask, err := client.EnsureCopy(ctx, CopySpec{SourcePath: "/cloud//source", DestinationPath: "/cloud//destination"})
	if err != nil {
		t.Fatalf("EnsureCopy: %v", err)
	}
	wantCopy := CopyTask{SourcePath: "/cloud/source", DestinationPath: "/cloud/destination", State: CopyScanning, Progress: .35}
	if copyTask != wantCopy {
		t.Fatalf("EnsureCopy task = %#v, want %#v", copyTask, wantCopy)
	}
	inspectedCopy, found, err := client.InspectCopy(ctx, "/cloud//source", "/cloud//destination")
	if err != nil || !found || inspectedCopy != wantCopy {
		t.Fatalf("InspectCopy = (%#v, %t, %v), want (%#v, true, nil)", inspectedCopy, found, err, wantCopy)
	}
	if err := client.CancelCopy(ctx, "/cloud//source", "/cloud//destination"); err != nil {
		t.Fatalf("CancelCopy: %v", err)
	}

	if contract.statusCalls != 1 || contract.tokenCalls != 1 || contract.listDirectoryCalls != 1 || contract.findCalls != 1 || contract.createFolderCalls != 2 || contract.addOfflineCalls != 1 || contract.removeOfflineCalls != 1 || contract.offlineListCalls != 3 || contract.copyFileCalls != 1 || contract.copyListCalls != 3 || contract.cancelCopyCalls != 1 {
		t.Fatalf("RPC calls = status:%d token:%d listDirectories:%d find:%d createFolder:%d addOffline:%d removeOffline:%d listOffline:%d copy:%d listCopy:%d cancelCopy:%d", contract.statusCalls, contract.tokenCalls, contract.listDirectoryCalls, contract.findCalls, contract.createFolderCalls, contract.addOfflineCalls, contract.removeOfflineCalls, contract.offlineListCalls, contract.copyFileCalls, contract.copyListCalls, contract.cancelCopyCalls)
	}
}
