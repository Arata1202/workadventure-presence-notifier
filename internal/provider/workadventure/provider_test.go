package workadventure

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
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

type reconnectFailureRoomManager struct {
	pb.UnimplementedRoomManagerServer
	streams    atomic.Int32
	closeFirst chan struct{}
}

type incompleteInitialRoomManager struct {
	pb.UnimplementedRoomManagerServer
}

func (*incompleteInitialRoomManager) GetRooms(context.Context, *emptypb.Empty) (*pb.RoomsList, error) {
	return &pb.RoomsList{RoomDescription: []*pb.RoomDescription{{RoomId: "room-a", NbUsers: 2}}}, nil
}

func (s *incompleteInitialRoomManager) AdminRoom(stream grpc.BidiStreamingServer[pb.AdminPusherToBackMessage, pb.ServerToAdminClientMessage]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&pb.ServerToAdminClientMessage{Message: &pb.ServerToAdminClientMessage_UserJoinedRoom{
		UserJoinedRoom: &pb.UserJoinedRoomMessage{Uuid: "user-1", Name: "Alice"},
	}}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

func (*reconnectFailureRoomManager) GetRooms(context.Context, *emptypb.Empty) (*pb.RoomsList, error) {
	return &pb.RoomsList{RoomDescription: []*pb.RoomDescription{{RoomId: "room-a", NbUsers: 1}}}, nil
}

func (s *reconnectFailureRoomManager) AdminRoom(stream grpc.BidiStreamingServer[pb.AdminPusherToBackMessage, pb.ServerToAdminClientMessage]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if s.streams.Add(1) == 1 {
		if err := stream.Send(&pb.ServerToAdminClientMessage{Message: &pb.ServerToAdminClientMessage_UserJoinedRoom{
			UserJoinedRoom: &pb.UserJoinedRoomMessage{Uuid: "user-1", Name: "Alice"},
		}}); err != nil {
			return err
		}
		<-s.closeFirst
		return nil
	}
	<-stream.Context().Done()
	return nil
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

func TestProviderDoesNotPublishIncompleteInitialSnapshot(t *testing.T) {
	listener := bufconn.Listen(testBufferSize)
	manager := &incompleteInitialRoomManager{}
	server := grpc.NewServer()
	pb.RegisterRoomManagerServer(server, manager)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	provider := New(Options{
		Address: "passthrough:///bufnet", RoomDiscoveryInterval: time.Hour,
		ReconnectInterval: 10 * time.Millisecond, InitialSyncTimeout: 30 * time.Millisecond, RequestTimeout: time.Second,
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() })},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- provider.Run(ctx) }()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer readyCancel()
	if err := provider.WaitReady(readyCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady() error = %v, want deadline exceeded", err)
	}
	if _, err := provider.Snapshot(); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Snapshot() error = %v, want ErrNotReady", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProviderRetainsLastSnapshotDuringReconnectSync(t *testing.T) {
	listener := bufconn.Listen(testBufferSize)
	manager := &reconnectFailureRoomManager{closeFirst: make(chan struct{})}
	server := grpc.NewServer()
	pb.RegisterRoomManagerServer(server, manager)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := New(Options{
		Address: "passthrough:///bufnet", RoomDiscoveryInterval: time.Hour,
		ReconnectInterval: 10 * time.Millisecond, InitialSyncTimeout: 500 * time.Millisecond, RequestTimeout: time.Second,
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() })},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- provider.Run(ctx) }()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	defer readyCancel()
	if err := provider.WaitReady(readyCtx); err != nil {
		t.Fatal(err)
	}
	close(manager.closeFirst)
	deadline := time.Now().Add(time.Second)
	for manager.streams.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if manager.streams.Load() < 2 {
		t.Fatal("provider did not reconnect")
	}
	snapshot, err := provider.Snapshot()
	if err != nil || snapshot.Count() != 1 || snapshot.Users[0].Name != "Alice" {
		t.Fatalf("Snapshot() during reconnect = %#v, %v", snapshot, err)
	}
	time.Sleep(550 * time.Millisecond)
	snapshot, err = provider.Snapshot()
	if err != nil || snapshot.Count() != 1 || snapshot.Users[0].Name != "Alice" {
		t.Fatalf("Snapshot() after failed reconnect sync = %#v, %v", snapshot, err)
	}
	select {
	case <-provider.Changes():
		t.Fatal("reconnect acquisition emitted a false presence change")
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type churnRoomManager struct {
	pb.UnimplementedRoomManagerServer
}

func (*churnRoomManager) GetRooms(context.Context, *emptypb.Empty) (*pb.RoomsList, error) {
	return &pb.RoomsList{RoomDescription: []*pb.RoomDescription{{RoomId: "room-a", NbUsers: 1}}}, nil
}

func (*churnRoomManager) AdminRoom(stream grpc.BidiStreamingServer[pb.AdminPusherToBackMessage, pb.ServerToAdminClientMessage]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	events := []*pb.ServerToAdminClientMessage{
		{Message: &pb.ServerToAdminClientMessage_UserJoinedRoom{UserJoinedRoom: &pb.UserJoinedRoomMessage{Uuid: "user-a", Name: "Alice"}}},
		{Message: &pb.ServerToAdminClientMessage_UserLeftRoom{UserLeftRoom: &pb.UserLeftRoomMessage{Uuid: "user-a"}}},
		{Message: &pb.ServerToAdminClientMessage_UserJoinedRoom{UserJoinedRoom: &pb.UserJoinedRoomMessage{Uuid: "user-b", Name: "Bob"}}},
	}
	for _, event := range events {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	return nil
}

func TestProviderSynchronizesCurrentConnectionsAfterInitialChurn(t *testing.T) {
	listener := bufconn.Listen(testBufferSize)
	server := grpc.NewServer()
	pb.RegisterRoomManagerServer(server, &churnRoomManager{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	provider := New(Options{
		Address: "passthrough:///bufnet", RoomDiscoveryInterval: time.Hour,
		ReconnectInterval: 10 * time.Millisecond, InitialSyncTimeout: 30 * time.Millisecond, RequestTimeout: time.Second,
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() })},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- provider.Run(ctx) }()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	defer readyCancel()
	if err := provider.WaitReady(readyCtx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := provider.Snapshot()
	if err != nil || snapshot.Count() != 1 || snapshot.Users[0].ID != "user-b" {
		t.Fatalf("Snapshot() = %#v, %v; want only user-b", snapshot, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSafeProviderErrorDoesNotExposeTransportDetails(t *testing.T) {
	if got := safeProviderError(errors.New("dial 192.0.2.1:50051: secret detail")); got != "transport_error" {
		t.Fatalf("safeProviderError() = %q", got)
	}
}

type validationChurnRoomManager struct {
	pb.UnimplementedRoomManagerServer
	calls             atomic.Int32
	validationStarted chan struct{}
	eventsSent        chan struct{}
}

func (s *validationChurnRoomManager) GetRooms(context.Context, *emptypb.Empty) (*pb.RoomsList, error) {
	if s.calls.Add(1) == 2 {
		close(s.validationStarted)
		<-s.eventsSent
	}
	return &pb.RoomsList{RoomDescription: []*pb.RoomDescription{{RoomId: "room-a", NbUsers: 1}}}, nil
}

func (s *validationChurnRoomManager) AdminRoom(stream grpc.BidiStreamingServer[pb.AdminPusherToBackMessage, pb.ServerToAdminClientMessage]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&pb.ServerToAdminClientMessage{Message: &pb.ServerToAdminClientMessage_UserJoinedRoom{
		UserJoinedRoom: &pb.UserJoinedRoomMessage{Uuid: "user-a", Name: "Alice"},
	}}); err != nil {
		return err
	}
	<-s.validationStarted
	for _, event := range []*pb.ServerToAdminClientMessage{
		{Message: &pb.ServerToAdminClientMessage_UserLeftRoom{UserLeftRoom: &pb.UserLeftRoomMessage{Uuid: "user-a"}}},
		{Message: &pb.ServerToAdminClientMessage_UserJoinedRoom{UserJoinedRoom: &pb.UserJoinedRoomMessage{Uuid: "user-b", Name: "Bob"}}},
	} {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	close(s.eventsSent)
	<-stream.Context().Done()
	return nil
}

func TestProviderDoesNotCommitWhileValidationEventsAreQueued(t *testing.T) {
	listener := bufconn.Listen(testBufferSize)
	manager := &validationChurnRoomManager{validationStarted: make(chan struct{}), eventsSent: make(chan struct{})}
	server := grpc.NewServer()
	pb.RegisterRoomManagerServer(server, manager)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	provider := New(Options{
		Address: "passthrough:///bufnet", RoomDiscoveryInterval: time.Hour,
		ReconnectInterval: 10 * time.Millisecond, InitialSyncTimeout: 30 * time.Millisecond, RequestTimeout: time.Second,
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() })},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- provider.Run(ctx) }()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	defer readyCancel()
	if err := provider.WaitReady(readyCtx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := provider.Snapshot()
	if err != nil || snapshot.Count() != 1 || snapshot.Users[0].ID != "user-b" {
		t.Fatalf("Snapshot() = %#v, %v; want only user-b", snapshot, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
