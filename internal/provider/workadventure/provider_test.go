package workadventure

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	pb "github.com/Arata1202/workadventure-presence-notifier/internal/proto/workadventure/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

const testBufferSize = 1024 * 1024

type fakeRoomManager struct {
	pb.UnimplementedRoomManagerServer
}

func (fakeRoomManager) GetRooms(context.Context, *emptypb.Empty) (*pb.RoomsList, error) {
	return &pb.RoomsList{RoomDescription: []*pb.RoomDescription{
		{RoomId: "https://example.com/maps/office.json", NbUsers: 2},
	}}, nil
}

func (fakeRoomManager) AdminRoom(stream grpc.BidiStreamingServer[pb.AdminPusherToBackMessage, pb.ServerToAdminClientMessage]) error {
	subscription, err := stream.Recv()
	if err != nil {
		return err
	}
	if subscription.GetSubscribeToRoom() == "" {
		return io.ErrUnexpectedEOF
	}

	for _, user := range []*pb.UserJoinedRoomMessage{
		{Uuid: "user-2", Name: "Bob"},
		{Uuid: "user-1", Name: "Alice"},
	} {
		if err := stream.Send(&pb.ServerToAdminClientMessage{
			Message: &pb.ServerToAdminClientMessage_UserJoinedRoom{UserJoinedRoom: user},
		}); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	return nil
}

func TestProviderCollectsPresence(t *testing.T) {
	listener := bufconn.Listen(testBufferSize)
	server := grpc.NewServer()
	pb.RegisterRoomManagerServer(server, fakeRoomManager{})
	go func() {
		if err := server.Serve(listener); err != nil {
			t.Logf("gRPC server stopped: %v", err)
		}
	}()
	defer server.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	provider := New(Options{
		Address:               "passthrough:///bufnet",
		RoomDiscoveryInterval: time.Hour,
		ReconnectInterval:     10 * time.Millisecond,
		InitialSyncTimeout:    time.Second,
		RequestTimeout:        time.Second,
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}),
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	runErrors := make(chan error, 1)
	go func() { runErrors <- provider.Run(ctx) }()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readyCancel()
	if err := provider.WaitReady(readyCtx); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}

	snapshot, err := provider.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Count() != 2 {
		t.Fatalf("snapshot.Count() = %d, want 2", snapshot.Count())
	}
	if snapshot.Users[0].Name != "Alice" || snapshot.Users[1].Name != "Bob" {
		t.Fatalf("snapshot.Users = %#v", snapshot.Users)
	}

	cancel()
	select {
	case err := <-runErrors:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop")
	}
}

func TestReconcileWorkersUpdatesExpectedUsers(t *testing.T) {
	provider := New(Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	worker := &roomWorker{cancel: func() {}}
	worker.expectedUsers.Store(1)
	provider.workers["room-a"] = worker

	provider.reconcileWorkers(context.Background(), nil, map[string]int{"room-a": 3})

	if got := worker.expectedUsers.Load(); got != 3 {
		t.Fatalf("expectedUsers = %d, want 3", got)
	}
}
