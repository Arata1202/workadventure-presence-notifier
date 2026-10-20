package workadventure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
	pb "github.com/Arata1202/workadventure-presence-notifier/internal/proto/workadventure/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

type Options struct {
	Address               string
	RoomDiscoveryInterval time.Duration
	ReconnectInterval     time.Duration
	InitialSyncTimeout    time.Duration
	RequestTimeout        time.Duration
	DialOptions           []grpc.DialOption
}

type roomWorker struct {
	cancel        context.CancelFunc
	expectedUsers atomic.Int64
}

type Provider struct {
	options Options
	logger  *slog.Logger
	store   *store

	mu      sync.Mutex
	workers map[string]*roomWorker
}

func New(options Options, logger *slog.Logger) *Provider {
	return &Provider{
		options: options,
		logger:  logger,
		store:   newStore(),
		workers: make(map[string]*roomWorker),
	}
}

func (p *Provider) Run(ctx context.Context) error {
	dialOptions := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	dialOptions = append(dialOptions, p.options.DialOptions...)
	connection, err := grpc.NewClient(p.options.Address, dialOptions...)
	if err != nil {
		return fmt.Errorf("create WorkAdventure gRPC client: %w", err)
	}
	defer connection.Close()
	defer p.stopWorkers()

	client := pb.NewRoomManagerClient(connection)
	p.discover(ctx, client)

	ticker := time.NewTicker(p.options.RoomDiscoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.discover(ctx, client)
		}
	}
}

func (p *Provider) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.store.waitReady():
		return nil
	}
}

func (p *Provider) Snapshot() (presence.Snapshot, error) {
	return p.store.snapshot()
}

func (p *Provider) Changes() <-chan struct{} {
	return p.store.changes()
}

func (p *Provider) discover(ctx context.Context, client pb.RoomManagerClient) {
	requestCtx, cancel := context.WithTimeout(ctx, p.options.RequestTimeout)
	defer cancel()

	rooms, err := client.GetRooms(requestCtx, &emptypb.Empty{})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.logger.Warn("failed to discover WorkAdventure rooms", "error", err)
		}
		return
	}

	roomIDs := make([]string, 0, len(rooms.GetRoomDescription()))
	active := make(map[string]int, len(rooms.GetRoomDescription()))
	for _, room := range rooms.GetRoomDescription() {
		if room.GetRoomId() == "" {
			continue
		}
		roomIDs = append(roomIDs, room.GetRoomId())
		active[room.GetRoomId()] = int(room.GetNbUsers())
	}

	p.store.setRooms(roomIDs)
	p.reconcileWorkers(ctx, client, active)
	p.logger.Debug("discovered WorkAdventure rooms", "rooms", len(roomIDs))
}

func (p *Provider) reconcileWorkers(ctx context.Context, client pb.RoomManagerClient, active map[string]int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for roomID, worker := range p.workers {
		if expectedUsers, ok := active[roomID]; ok {
			worker.expectedUsers.Store(int64(expectedUsers))
			continue
		}
		worker.cancel()
		delete(p.workers, roomID)
	}

	for roomID, expectedUsers := range active {
		if _, ok := p.workers[roomID]; ok {
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		worker := &roomWorker{cancel: cancel}
		worker.expectedUsers.Store(int64(expectedUsers))
		p.workers[roomID] = worker
		go p.runRoom(workerCtx, client, roomID, &worker.expectedUsers)
	}
}

func (p *Provider) stopWorkers() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for roomID, worker := range p.workers {
		worker.cancel()
		delete(p.workers, roomID)
	}
}

func (p *Provider) runRoom(ctx context.Context, client pb.RoomManagerClient, roomID string, expectedUsers *atomic.Int64) {
	for {
		p.store.resetRoom(roomID)
		err := p.subscribeRoom(ctx, client, roomID, expectedUsers)
		if ctx.Err() != nil {
			return
		}
		p.logger.Warn("WorkAdventure room subscription ended", "error", err)
		if !sleep(ctx, p.options.ReconnectInterval) {
			return
		}
	}
}

type receiveResult struct {
	message *pb.ServerToAdminClientMessage
	err     error
}

func (p *Provider) subscribeRoom(ctx context.Context, client pb.RoomManagerClient, roomID string, expectedUsers *atomic.Int64) error {
	stream, err := client.AdminRoom(ctx)
	if err != nil {
		return fmt.Errorf("open adminRoom stream: %w", err)
	}
	defer stream.CloseSend()

	if err := stream.Send(&pb.AdminPusherToBackMessage{
		Message: &pb.AdminPusherToBackMessage_SubscribeToRoom{SubscribeToRoom: roomID},
	}); err != nil {
		return fmt.Errorf("subscribe to room: %w", err)
	}

	results := make(chan receiveResult, 1)
	go func() {
		for {
			message, receiveErr := stream.Recv()
			select {
			case results <- receiveResult{message: message, err: receiveErr}:
			case <-ctx.Done():
				return
			}
			if receiveErr != nil {
				return
			}
		}
	}()

	timer := time.NewTimer(p.options.InitialSyncTimeout)
	defer timer.Stop()
	waitForTimeout := expectedUsers.Load() <= 0
	ready := false
	joinedEvents := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if !ready {
				ready = true
				p.store.setRoomReady(roomID)
			}
		case result := <-results:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) || errors.Is(result.err, context.Canceled) {
					return nil
				}
				return fmt.Errorf("receive room presence: %w", result.err)
			}
			switch event := result.message.GetMessage().(type) {
			case *pb.ServerToAdminClientMessage_UserJoinedRoom:
				joinedEvents++
				p.store.addUser(roomID, event.UserJoinedRoom.GetUuid(), event.UserJoinedRoom.GetName())
				if !ready && !waitForTimeout && int64(joinedEvents) >= expectedUsers.Load() {
					ready = true
					p.store.setRoomReady(roomID)
				}
			case *pb.ServerToAdminClientMessage_UserLeftRoom:
				p.store.removeUser(roomID, event.UserLeftRoom.GetUuid())
			}
		}
	}
}

func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
