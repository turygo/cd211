// Package clouddrive is the retry-safe gRPC client for CloudDrive2 offline
// download, copy, and filesystem operations.
package clouddrive

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/turygo/cd211/internal/clouddrive/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ErrorKind string

const (
	ErrorTransient       ErrorKind = "transient"
	ErrorPermanent       ErrorKind = "permanent"
	ErrorUnauthorized    ErrorKind = "unauthorized"
	ErrorInvalidResponse ErrorKind = "invalid_response"
)

type Error struct {
	Operation string
	Kind      ErrorKind
	detail    string
	cause     error
}

func (e *Error) Error() string {
	message := "clouddrive " + e.Operation + ": " + string(e.Kind)
	if e.detail != "" {
		return message + ": " + e.detail
	}
	return message
}
func (e *Error) Unwrap() error { return e.cause }

type OfflineState string

const (
	OfflineInit        OfflineState = "INIT"
	OfflineDownloading OfflineState = "DOWNLOADING"
	OfflineFinished    OfflineState = "FINISHED"
	OfflineError       OfflineState = "ERROR"
)

type OfflineTask struct {
	Name, InfoHash, SourcePath string
	State                      OfflineState
	Progress                   float64
	Size                       int64
}

type CopyState string

const (
	CopyPending   CopyState = "PENDING"
	CopyScanning  CopyState = "SCANNING"
	CopyScanned   CopyState = "SCANNED"
	CopyCompleted CopyState = "COMPLETED"
	CopyFailed    CopyState = "FAILED"
)

type CopyTask struct {
	SourcePath, DestinationPath string
	State                       CopyState
	Progress                    float64
	ErrorCount                  int
}

type OfflineSpec struct {
	SubmissionURI, CloudFolder, Hash string
}

type Directory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type CopySpec struct {
	SourcePath, DestinationPath string
}

type rpcClient interface {
	GetToken(context.Context, *pb.GetTokenRequest, ...grpc.CallOption) (*pb.JWTToken, error)
	GetSubFiles(context.Context, *pb.ListSubFileRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[pb.SubFilesReply], error)
	FindFileByPath(context.Context, *pb.FindFileByPathRequest, ...grpc.CallOption) (*pb.CloudDriveFile, error)
	CreateFolder(context.Context, *pb.CreateFolderRequest, ...grpc.CallOption) (*pb.CreateFolderResult, error)
	AddOfflineFiles(context.Context, *pb.AddOfflineFileRequest, ...grpc.CallOption) (*pb.FileOperationResult, error)
	RemoveOfflineFiles(context.Context, *pb.RemoveOfflineFilesRequest, ...grpc.CallOption) (*pb.FileOperationResult, error)
	ListOfflineFilesByPath(context.Context, *pb.FileRequest, ...grpc.CallOption) (*pb.OfflineFileListResult, error)
	CopyFile(context.Context, *pb.CopyFileRequest, ...grpc.CallOption) (*pb.FileOperationResult, error)
	GetCopyTasks(context.Context, *emptypb.Empty, ...grpc.CallOption) (*pb.GetCopyTaskResult, error)
	CancelCopyTask(context.Context, *pb.CopyTaskRequest, ...grpc.CallOption) (*emptypb.Empty, error)
}

type systemInfoClient interface {
	GetSystemInfo(context.Context, *emptypb.Empty, ...grpc.CallOption) (*pb.CloudDriveSystemInfo, error)
}

type Client struct {
	rpc      rpcClient
	closer   io.Closer
	username string
	password string
	timeout  time.Duration
	now      func() time.Time

	tokenMu    sync.Mutex
	token      string
	tokenUntil time.Time
	closeOnce  sync.Once
	closeErr   error
}

func Dial(address, username, password string, timeout time.Duration, allowInsecure bool) (*Client, error) {
	address = strings.TrimSpace(address)
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return nil, newError("authenticate", ErrorPermanent, nil)
	}
	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: host})
	if allowInsecure {
		transport = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, newError("authenticate", classify(err), err)
	}
	client, err := New(pb.NewCloudDriveFileSrvClient(conn), conn, username, password, timeout, time.Now)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

func New(rpc rpcClient, closer io.Closer, username, password string, timeout time.Duration, now func() time.Time) (*Client, error) {
	username = strings.TrimSpace(username)
	if rpc == nil || closer == nil || now == nil || username == "" || strings.TrimSpace(password) == "" || timeout <= 0 {
		return nil, newError("authenticate", ErrorPermanent, nil)
	}
	return &Client{rpc: rpc, closer: closer, username: username, password: password, timeout: timeout, now: now}, nil
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.closer.Close() })
	return c.closeErr
}

// Check reports whether CloudDrive2 is logged in and ready without exposing
// its diagnostic message.
func (c *Client) Check(ctx context.Context) error {
	rpc, ok := c.rpc.(systemInfoClient)
	if !ok {
		return newError("system_status", ErrorInvalidResponse, nil)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := rpc.GetSystemInfo(callCtx, &emptypb.Empty{})
	if err != nil {
		return c.rpcError("system_status", err)
	}
	if response == nil {
		return newError("system_status", ErrorInvalidResponse, nil)
	}
	if !response.IsLogin || !response.SystemReady || response.GetHasError() {
		return newError("system_status", ErrorTransient, nil)
	}
	return nil
}

// Authenticate exercises the configured credentials by acquiring a token.
// Unlike Check, it performs the login round-trip, so wrong credentials are
// reported as ErrorUnauthorized (or the appropriate transport failure kind).
func (c *Client) Authenticate(ctx context.Context) error {
	_, err := c.tokenFor(ctx)
	return err
}

func (c *Client) FindFile(ctx context.Context, fullPath string) (*pb.CloudDriveFile, error) {
	fullPath, ok := cleanAbsolutePath(fullPath)
	if !ok || fullPath == "/" {
		return nil, newError("find_file", ErrorPermanent, nil)
	}
	callCtx, cancel, err := c.authorizedContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	file, rpcErr := c.rpc.FindFileByPath(callCtx, &pb.FindFileByPathRequest{ParentPath: path.Dir(fullPath), Path: path.Base(fullPath)})
	if rpcErr != nil {
		return nil, c.rpcError("find_file", rpcErr)
	}
	if file == nil {
		return nil, newError("find_file", ErrorInvalidResponse, nil)
	}
	if file.FullPathName != "" {
		responsePath, valid := cleanAbsolutePath(file.FullPathName)
		if !valid || responsePath != fullPath {
			return nil, newError("find_file", ErrorInvalidResponse, nil)
		}
	}
	return file, nil
}

// ListDirectories returns the immediate child directories of fullPath.
func (c *Client) ListDirectories(ctx context.Context, fullPath string) ([]Directory, error) {
	fullPath, ok := cleanAbsolutePath(fullPath)
	if !ok {
		return nil, newError("list_directories", ErrorPermanent, nil)
	}
	callCtx, cancel, err := c.authorizedContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	stream, rpcErr := c.rpc.GetSubFiles(callCtx, &pb.ListSubFileRequest{Path: fullPath})
	if rpcErr != nil {
		return nil, c.rpcError("list_directories", rpcErr)
	}

	directories := make([]Directory, 0)
	seen := make(map[string]struct{})
	for {
		reply, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, c.rpcError("list_directories", recvErr)
		}
		if reply == nil {
			return nil, newError("list_directories", ErrorInvalidResponse, nil)
		}
		for _, file := range reply.GetSubFiles() {
			if file == nil || !file.GetIsDirectory() {
				continue
			}
			childPath, valid := cleanAbsolutePath(file.GetFullPathName())
			if !valid || childPath == fullPath || path.Dir(childPath) != fullPath {
				return nil, newError("list_directories", ErrorInvalidResponse, nil)
			}
			if _, exists := seen[childPath]; exists {
				continue
			}
			seen[childPath] = struct{}{}
			directories = append(directories, Directory{Name: path.Base(childPath), Path: childPath})
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		left, right := strings.ToLower(directories[i].Name), strings.ToLower(directories[j].Name)
		if left == right {
			return directories[i].Path < directories[j].Path
		}
		return left < right
	})
	return directories, nil
}

// CreateDirectory creates one immediate child directory under parentPath.
// If the directory already exists, it is returned as a successful result.
func (c *Client) CreateDirectory(ctx context.Context, parentPath, name string) (Directory, error) {
	parentPath, parentOK := cleanAbsolutePath(parentPath)
	name = strings.TrimSpace(name)
	if !parentOK || name == "" || name == "." || name == ".." || path.Base(name) != name {
		return Directory{}, newError("create_folder", ErrorPermanent, nil)
	}
	fullPath := path.Join(parentPath, name)
	if fullPath == parentPath {
		return Directory{}, newError("create_folder", ErrorPermanent, nil)
	}
	if err := c.ensureCloudFolder(ctx, fullPath); err != nil {
		return Directory{}, err
	}
	return Directory{Name: name, Path: fullPath}, nil
}

func (c *Client) EnsureOffline(ctx context.Context, spec OfflineSpec) (OfflineTask, error) {
	folder, ok := cleanAbsolutePath(spec.CloudFolder)
	if !ok || !validLowerInfoHash(spec.Hash) || strings.TrimSpace(spec.SubmissionURI) == "" {
		return OfflineTask{}, newError("add_offline", ErrorPermanent, nil)
	}
	if err := c.ensureCloudFolder(ctx, folder); err != nil {
		return OfflineTask{}, err
	}
	if task, found, err := c.InspectOffline(ctx, folder, spec.Hash); err != nil || found {
		return task, err
	}
	callCtx, cancel, err := c.authorizedContext(ctx)
	if err != nil {
		return OfflineTask{}, err
	}
	defer cancel()
	result, rpcErr := c.rpc.AddOfflineFiles(callCtx, &pb.AddOfflineFileRequest{Urls: spec.SubmissionURI, ToFolder: folder})
	if rpcErr != nil {
		return c.recheckOffline(ctx, folder, spec.Hash, c.rpcError("add_offline", rpcErr))
	}
	if result == nil {
		return OfflineTask{}, newError("add_offline", ErrorInvalidResponse, nil)
	}
	if !result.Success {
		return c.recheckOffline(ctx, folder, spec.Hash, newError("add_offline", ErrorPermanent, nil))
	}
	task, found, inspectErr := c.InspectOffline(ctx, folder, spec.Hash)
	if inspectErr != nil {
		return OfflineTask{}, inspectErr
	}
	if found {
		return task, nil
	}
	return OfflineTask{InfoHash: spec.Hash, State: OfflineInit}, nil
}

// ensureCloudFolder creates the offline target folder when CloudDrive2 does not
// have it yet, because listing or adding offline files under a missing folder
// fails permanently. Only the leaf is created: a missing parent means CloudRoot
// itself is misconfigured, and silently building that tree on the cloud drive
// would hide the mistake.
func (c *Client) ensureCloudFolder(ctx context.Context, folder string) error {
	switch file, err := c.FindFile(ctx, folder); {
	case err == nil && file.GetIsDirectory():
		return nil
	case err == nil:
		return newError("create_folder", ErrorPermanent, nil)
	}
	// path.Dir only equals the input at the filesystem root, which has no parent
	// to create the folder in.
	parent, name := path.Dir(folder), path.Base(folder)
	if parent == folder {
		return newError("create_folder", ErrorPermanent, nil)
	}
	callCtx, cancel, err := c.authorizedContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	result, rpcErr := c.rpc.CreateFolder(callCtx, &pb.CreateFolderRequest{ParentPath: parent, FolderName: name})
	if rpcErr != nil {
		return c.recheckCloudFolder(ctx, folder, c.rpcError("create_folder", rpcErr))
	}
	if result.GetResult() == nil {
		return newError("create_folder", ErrorInvalidResponse, nil)
	}
	if !result.GetResult().GetSuccess() {
		return c.recheckCloudFolder(ctx, folder, newError("create_folder", ErrorPermanent, nil))
	}
	return nil
}

// recheckCloudFolder treats a folder that exists after a failed create as
// success, so a concurrent reconciler or a retried attempt is not an error.
func (c *Client) recheckCloudFolder(ctx context.Context, folder string, original error) error {
	if file, err := c.FindFile(ctx, folder); err == nil && file.GetIsDirectory() {
		return nil
	}
	return original
}

func (c *Client) recheckOffline(ctx context.Context, folder, hash string, original error) (OfflineTask, error) {
	task, found, err := c.InspectOffline(ctx, folder, hash)
	if found {
		return task, nil
	}
	if err != nil {
		return OfflineTask{}, original
	}
	if grpcErrorCode(original) == codes.AlreadyExists {
		return OfflineTask{InfoHash: hash, State: OfflineInit}, nil
	}
	return OfflineTask{}, original
}

func (c *Client) InspectOffline(ctx context.Context, cloudFolder, hash string) (OfflineTask, bool, error) {
	folder, ok := cleanAbsolutePath(cloudFolder)
	if !ok || !validLowerInfoHash(hash) {
		return OfflineTask{}, false, newError("list_offline", ErrorPermanent, nil)
	}
	callCtx, cancel, err := c.authorizedContext(ctx)
	if err != nil {
		return OfflineTask{}, false, err
	}
	defer cancel()
	result, rpcErr := c.rpc.ListOfflineFilesByPath(callCtx, &pb.FileRequest{Path: folder})
	if rpcErr != nil {
		return OfflineTask{}, false, c.rpcError("list_offline", rpcErr)
	}
	if result == nil {
		return OfflineTask{}, false, newError("list_offline", ErrorInvalidResponse, nil)
	}
	var matched *OfflineTask
	for _, item := range result.OfflineFiles {
		if item == nil {
			return OfflineTask{}, false, newError("list_offline", ErrorInvalidResponse, nil)
		}
		if !validInfoHash(item.InfoHash) || !strings.EqualFold(item.InfoHash, hash) {
			continue
		}
		task, mapErr := mapOffline(folder, hash, item)
		if mapErr != nil {
			return OfflineTask{}, false, newError("list_offline", ErrorInvalidResponse, mapErr)
		}
		if matched != nil && *matched != task {
			return OfflineTask{}, false, newError("list_offline", ErrorInvalidResponse, nil)
		}
		if matched == nil {
			matched = &task
		}
	}
	if matched == nil {
		return OfflineTask{}, false, nil
	}
	return *matched, true, nil
}

func (c *Client) CancelOffline(ctx context.Context, cloudFolder, hash string) error {
	folder, ok := cleanAbsolutePath(cloudFolder)
	if !ok || !validLowerInfoHash(hash) {
		return newError("cancel_offline", ErrorPermanent, nil)
	}
	callCtx, cancel, err := c.authorizedContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	result, rpcErr := c.rpc.RemoveOfflineFiles(callCtx, &pb.RemoveOfflineFilesRequest{Path: proto.String(folder), InfoHashes: []string{hash}, DeleteFiles: false})
	if rpcErr != nil {
		if status.Code(rpcErr) == codes.NotFound {
			return nil
		}
		return c.rpcError("cancel_offline", rpcErr)
	}
	if result == nil {
		return newError("cancel_offline", ErrorInvalidResponse, nil)
	}
	if !result.Success {
		_, found, inspectErr := c.InspectOffline(ctx, folder, hash)
		if inspectErr != nil {
			return inspectErr
		}
		if !found {
			return nil
		}
		return newError("cancel_offline", ErrorPermanent, nil)
	}
	return nil
}

func (c *Client) EnsureCopy(ctx context.Context, spec CopySpec) (CopyTask, error) {
	source, sourceOK := cleanAbsolutePath(spec.SourcePath)
	destination, destinationOK := cleanAbsolutePath(spec.DestinationPath)
	if !sourceOK || !destinationOK {
		return CopyTask{}, newError("add_copy", ErrorPermanent, nil)
	}
	if task, found, err := c.InspectCopy(ctx, source, destination); err != nil || found {
		return task, err
	}
	callCtx, cancel, err := c.authorizedContext(ctx)
	if err != nil {
		return CopyTask{}, err
	}
	defer cancel()
	result, rpcErr := c.rpc.CopyFile(callCtx, &pb.CopyFileRequest{TheFilePaths: []string{source}, DestPath: destination, ConflictPolicy: pb.CopyFileRequest_Skip.Enum(), HandleConflictRecursively: proto.Bool(false)})
	if rpcErr != nil {
		return c.recheckCopy(ctx, source, destination, c.rpcError("add_copy", rpcErr))
	}
	if result == nil {
		return CopyTask{}, newError("add_copy", ErrorInvalidResponse, nil)
	}
	if !result.Success {
		return c.recheckCopy(ctx, source, destination, newResultError("add_copy", ErrorPermanent, result.GetErrorMessage()))
	}
	task, found, inspectErr := c.InspectCopy(ctx, source, destination)
	if inspectErr != nil {
		return CopyTask{}, inspectErr
	}
	if found {
		return task, nil
	}
	return CopyTask{SourcePath: source, DestinationPath: destination, State: CopyPending}, nil
}

func (c *Client) recheckCopy(ctx context.Context, source, destination string, original error) (CopyTask, error) {
	task, found, err := c.InspectCopy(ctx, source, destination)
	if found {
		return task, nil
	}
	if err != nil {
		return CopyTask{}, original
	}
	if grpcErrorCode(original) == codes.AlreadyExists {
		return CopyTask{SourcePath: source, DestinationPath: destination, State: CopyPending}, nil
	}
	return CopyTask{}, original
}

func (c *Client) InspectCopy(ctx context.Context, sourcePath, destinationPath string) (CopyTask, bool, error) {
	source, sourceOK := cleanAbsolutePath(sourcePath)
	destination, destinationOK := cleanAbsolutePath(destinationPath)
	if !sourceOK || !destinationOK {
		return CopyTask{}, false, newError("list_copy", ErrorPermanent, nil)
	}
	callCtx, cancel, err := c.authorizedContext(ctx)
	if err != nil {
		return CopyTask{}, false, err
	}
	defer cancel()
	result, rpcErr := c.rpc.GetCopyTasks(callCtx, &emptypb.Empty{})
	if rpcErr != nil {
		return CopyTask{}, false, c.rpcError("list_copy", rpcErr)
	}
	if result == nil {
		return CopyTask{}, false, newError("list_copy", ErrorInvalidResponse, nil)
	}
	var matched *CopyTask
	for _, item := range result.CopyTasks {
		if item == nil {
			return CopyTask{}, false, newError("list_copy", ErrorInvalidResponse, nil)
		}
		itemSource, sourceValid := cleanAbsolutePath(item.SourcePath)
		itemDestination, destinationValid := cleanAbsolutePath(item.DestPath)
		if !sourceValid || !destinationValid || itemSource != source || itemDestination != destination {
			continue
		}
		task, mapErr := mapCopy(itemSource, itemDestination, item)
		if mapErr != nil {
			return CopyTask{}, false, newError("list_copy", ErrorInvalidResponse, mapErr)
		}
		if matched != nil && *matched != task {
			return CopyTask{}, false, newError("list_copy", ErrorInvalidResponse, nil)
		}
		if matched == nil {
			matched = &task
		}
	}
	if matched == nil {
		return CopyTask{}, false, nil
	}
	return *matched, true, nil
}

func (c *Client) CancelCopy(ctx context.Context, sourcePath, destinationPath string) error {
	source, sourceOK := cleanAbsolutePath(sourcePath)
	destination, destinationOK := cleanAbsolutePath(destinationPath)
	if !sourceOK || !destinationOK {
		return newError("cancel_copy", ErrorPermanent, nil)
	}
	callCtx, cancel, err := c.authorizedContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	result, rpcErr := c.rpc.CancelCopyTask(callCtx, &pb.CopyTaskRequest{SourcePath: source, DestPath: destination})
	if rpcErr != nil {
		if status.Code(rpcErr) == codes.NotFound {
			return nil
		}
		return c.rpcError("cancel_copy", rpcErr)
	}
	if result == nil {
		return newError("cancel_copy", ErrorInvalidResponse, nil)
	}
	return nil
}

func (c *Client) authorizedContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	token, err := c.tokenFor(parent)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), cancel, nil
}

func (c *Client) tokenFor(parent context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	now := c.now()
	if c.token != "" && now.Before(c.tokenUntil) {
		return c.token, nil
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.MD{})
	response, err := c.rpc.GetToken(ctx, &pb.GetTokenRequest{UserName: c.username, Password: c.password})
	if err != nil {
		c.token = ""
		c.tokenUntil = time.Time{}
		return "", newError("authenticate", classify(err), err)
	}
	if response == nil {
		return "", newError("authenticate", ErrorInvalidResponse, nil)
	}
	if !response.Success {
		return "", newError("authenticate", ErrorUnauthorized, nil)
	}
	if response.Expiration == nil || response.Expiration.CheckValid() != nil || response.Token == "" {
		return "", newError("authenticate", ErrorInvalidResponse, nil)
	}
	expires := response.Expiration.AsTime()
	if !expires.After(now) {
		return "", newError("authenticate", ErrorInvalidResponse, nil)
	}
	lifetime := expires.Sub(now)
	if lifetime < 2*time.Minute {
		c.tokenUntil = now.Add(lifetime / 2)
	} else {
		c.tokenUntil = expires.Add(-time.Minute)
	}
	c.token = response.Token
	return c.token, nil
}

func (c *Client) rpcError(operation string, err error) error {
	kind := classify(err)
	if kind == ErrorUnauthorized {
		c.tokenMu.Lock()
		c.token = ""
		c.tokenUntil = time.Time{}
		c.tokenMu.Unlock()
	}
	return newError(operation, kind, err)
}

func newError(operation string, kind ErrorKind, cause error) error {
	return &Error{Operation: operation, Kind: kind, cause: cause}
}

func newResultError(operation string, kind ErrorKind, detail string) error {
	return &Error{Operation: operation, Kind: kind, detail: strings.TrimSpace(detail)}
}

func classify(err error) ErrorKind {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorTransient
	}
	grpcStatus, ok := status.FromError(err)
	if !ok {
		return ErrorInvalidResponse
	}
	switch grpcStatus.Code() {
	case codes.Canceled, codes.DeadlineExceeded, codes.Unavailable, codes.ResourceExhausted, codes.Aborted, codes.Internal, codes.Unknown:
		return ErrorTransient
	case codes.Unauthenticated, codes.PermissionDenied:
		return ErrorUnauthorized
	case codes.InvalidArgument, codes.FailedPrecondition, codes.AlreadyExists, codes.NotFound, codes.OutOfRange:
		return ErrorPermanent
	default:
		return ErrorInvalidResponse
	}
}

func mapOffline(folder, hash string, file *pb.OfflineFile) (OfflineTask, error) {
	if !safeName(file.Name) {
		return OfflineTask{}, errors.New("invalid name")
	}
	state, ok := offlineState(file.Status)
	if !ok {
		return OfflineTask{}, errors.New("invalid status")
	}
	progress, ok := normalizedOfflineProgress(file.PercendDone)
	if !ok {
		return OfflineTask{}, errors.New("invalid progress")
	}
	if file.Size > math.MaxInt64 {
		return OfflineTask{}, errors.New("invalid size")
	}
	return OfflineTask{Name: file.Name, InfoHash: hash, SourcePath: path.Join(folder, file.Name), State: state, Progress: progress, Size: int64(file.Size)}, nil
}

func offlineState(status pb.OfflineFileStatus) (OfflineState, bool) {
	switch status {
	case pb.OfflineFileStatus_OFFLINE_INIT:
		return OfflineInit, true
	case pb.OfflineFileStatus_OFFLINE_DOWNLOADING:
		return OfflineDownloading, true
	case pb.OfflineFileStatus_OFFLINE_FINISHED:
		return OfflineFinished, true
	case pb.OfflineFileStatus_OFFLINE_ERROR:
		return OfflineError, true
	default:
		return "", false
	}
}

func normalizedOfflineProgress(progress float64) (float64, bool) {
	if math.IsNaN(progress) || math.IsInf(progress, 0) || progress < 0 {
		return 0, false
	}
	if progress <= 1 {
		return progress, true
	}
	if progress <= 100 {
		return math.Min(1, math.Max(0, progress/100)), true
	}
	return 0, false
}

func mapCopy(source, destination string, task *pb.CopyTask) (CopyTask, error) {
	state, ok := copyState(task.Status)
	if !ok {
		return CopyTask{}, errors.New("invalid status")
	}
	var progress float64
	if task.TotalBytes == 0 {
		if task.UploadedBytes != 0 {
			return CopyTask{}, errors.New("invalid progress")
		}
		if state == CopyCompleted {
			progress = 1
		}
	} else {
		if task.UploadedBytes > task.TotalBytes {
			return CopyTask{}, errors.New("invalid progress")
		}
		progress = math.Min(1, math.Max(0, float64(task.UploadedBytes)/float64(task.TotalBytes)))
	}
	for _, item := range task.Errors {
		if item == nil {
			return CopyTask{}, errors.New("invalid error")
		}
	}
	return CopyTask{SourcePath: source, DestinationPath: destination, State: state, Progress: progress, ErrorCount: len(task.Errors)}, nil
}

func copyState(status pb.CopyTask_TaskStatus) (CopyState, bool) {
	switch status {
	case pb.CopyTask_Pending:
		return CopyPending, true
	case pb.CopyTask_Scanning:
		return CopyScanning, true

	case pb.CopyTask_Scanned:
		return CopyScanned, true
	case pb.CopyTask_Completed:
		return CopyCompleted, true
	case pb.CopyTask_Failed:
		return CopyFailed, true
	default:
		return "", false
	}
}

func grpcErrorCode(err error) codes.Code {
	var provider interface {
		GRPCStatus() *status.Status
	}
	if errors.As(err, &provider) {
		return provider.GRPCStatus().Code()
	}
	return codes.Unknown
}

func cleanAbsolutePath(value string) (string, bool) {
	if !validPath(value) || !path.IsAbs(value) {
		return "", false
	}
	return path.Clean(value), true
}

func validPath(value string) bool {
	if !utf8.ValidString(value) || value == "" || strings.ContainsAny(value, "\x00\\") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func safeName(value string) bool {
	return validPath(value) && value != "." && value != ".." && !strings.Contains(value, "/")
}

func validLowerInfoHash(value string) bool {
	if len(value) != 40 {
		return false
	}
	for i := range len(value) {
		if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	return true
}

func validInfoHash(value string) bool {
	if len(value) != 40 {
		return false
	}
	for i := range len(value) {
		if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f' || value[i] >= 'A' && value[i] <= 'F') {
			return false
		}
	}
	return true
}
