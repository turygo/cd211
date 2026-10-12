package clouddrive

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/clouddrive/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestRemoveCopyConfirmsExactIdentity(t *testing.T) {
	for _, item := range []struct {
		name      string
		state     pb.CopyTask_TaskStatus
		lostReply bool
	}{
		{"completed", pb.CopyTask_Completed, false},
		{"failed", pb.CopyTask_Failed, false},
		{"cancellation transition", pb.CopyTask_TaskStatus(99), false},
		{"lost cancellation response", pb.CopyTask_Completed, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			base := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
			target := &pb.CopyTask{SourcePath: "/cloud/item", DestPath: "/downloads/owned", Status: item.state}
			sentinel := &pb.CopyTask{SourcePath: "/cloud/item", DestPath: "/downloads/other", Status: pb.CopyTask_Completed}
			tasks := []*pb.CopyTask{target, sentinel}
			rpc := &fakeRPC{}
			rpc.getToken = func(context.Context, *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
			rpc.getCopy = func(context.Context, *emptypb.Empty) (*pb.GetCopyTaskResult, error) {
				return &pb.GetCopyTaskResult{CopyTasks: tasks}, nil
			}
			rpc.cancelCopy = func(_ context.Context, req *pb.CopyTaskRequest) (*emptypb.Empty, error) {
				if req.SourcePath != target.SourcePath || req.DestPath != target.DestPath {
					t.Fatal("attempted to cancel an unrelated task")
				}
				tasks = []*pb.CopyTask{sentinel}
				if item.lostReply {
					return nil, status.Error(codes.DeadlineExceeded, "reply lost after removal")
				}
				return &emptypb.Empty{}, nil
			}
			client := newTestClient(t, rpc, func() time.Time { return base })
			for range 2 {
				if err := client.RemoveCopy(context.Background(), "/cloud//item", "/downloads/owned"); err != nil {
					t.Fatal(err)
				}
			}
			if len(tasks) != 1 || tasks[0] != sentinel {
				t.Fatalf("unrelated task was changed: %+v", tasks)
			}
		})
	}
}

func TestRemoveCopyDoesNotAcceptUnconfirmedCancellation(t *testing.T) {
	base := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	present := true
	allowRemoval := false
	rpc := &fakeRPC{}
	rpc.getToken = func(context.Context, *pb.GetTokenRequest) (*pb.JWTToken, error) { return token(base), nil }
	rpc.getCopy = func(context.Context, *emptypb.Empty) (*pb.GetCopyTaskResult, error) {
		result := &pb.GetCopyTaskResult{}
		if present {
			result.CopyTasks = []*pb.CopyTask{{SourcePath: "/cloud/item", DestPath: "/downloads/owned", Status: pb.CopyTask_Completed}}
		}
		return result, nil
	}
	rpc.cancelCopy = func(context.Context, *pb.CopyTaskRequest) (*emptypb.Empty, error) {
		if allowRemoval {
			present = false
		}
		return &emptypb.Empty{}, nil
	}
	client := newTestClient(t, rpc, func() time.Time { return base })
	client.timeout = 10 * time.Millisecond
	err := client.RemoveCopy(context.Background(), "/cloud/item", "/downloads/owned")
	var cloudErr *Error
	if !errors.As(err, &cloudErr) || cloudErr.Kind != ErrorTemporary || !present {
		t.Fatalf("unconfirmed cancellation accepted: error=%v present=%t", err, present)
	}
	allowRemoval = true
	if err := client.RemoveCopy(context.Background(), "/cloud/item", "/downloads/owned"); err != nil || present {
		t.Fatalf("retry did not confirm removal: error=%v present=%t", err, present)
	}
}
