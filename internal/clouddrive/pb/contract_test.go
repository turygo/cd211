package pb

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

const bufconnSize = 1024 * 1024

type fakeCloudDriveFileSrv struct {
	UnimplementedCloudDriveFileSrvServer
}

func (fakeCloudDriveFileSrv) GetSystemInfo(context.Context, *emptypb.Empty) (*CloudDriveSystemInfo, error) {
	return &CloudDriveSystemInfo{
		IsLogin:     true,
		UserName:    "fake",
		SystemReady: true,
	}, nil
}

func TestCloudDriveFileSrvGetSystemInfoOverBufconn(t *testing.T) {
	listener := bufconn.Listen(bufconnSize)
	server := grpc.NewServer()
	RegisterCloudDriveFileSrvServer(server, fakeCloudDriveFileSrv{})

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	var clientConn *grpc.ClientConn
	t.Cleanup(func() {
		if clientConn != nil {
			if err := clientConn.Close(); err != nil {
				t.Errorf("close gRPC client: %v", err)
			}
		}
		server.Stop()
		if err := listener.Close(); err != nil {
			t.Errorf("close bufconn listener: %v", err)
		}
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("serve gRPC server: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("gRPC server did not stop")
		}
	})

	var err error
	clientConn, err = grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("create gRPC client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	response, err := NewCloudDriveFileSrvClient(clientConn).GetSystemInfo(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("get system info: %v", err)
	}
	if response == nil {
		t.Fatal("get system info returned nil response")
	}
	if !response.GetIsLogin() {
		t.Error("get system info IsLogin = false, want true")
	}
	if response.GetUserName() != "fake" {
		t.Errorf("get system info UserName = %q, want %q", response.GetUserName(), "fake")
	}
	if !response.GetSystemReady() {
		t.Error("get system info SystemReady = false, want true")
	}
}
