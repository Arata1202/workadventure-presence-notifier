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
	cancel context.CancelFunc
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
			p.logger.Warn("failed to discover WorkAdventure rooms", "reason", safeProviderError(err))
		}
		return
	}

	roomIDs := make([]string, 0, len(rooms.GetRoomDescription()))
	active := make(map[string]struct{}, len(rooms.GetRoomDescription()))
	for _, room := range rooms.GetRoomDescription() {
		if room.GetRoomId() == "" {
			continue
		}
		roomIDs = append(roomIDs, room.GetRoomId())
		active[room.GetRoomId()] = struct{}{}
	}

	p.store.setRooms(roomIDs)
	p.reconcileWorkers(ctx, client, active)
	p.logger.Debug("discovered WorkAdventure rooms", "rooms", len(roomIDs))
}

func (p *Provider) reconcileWorkers(ctx context.Context, client pb.RoomManagerClient, active map[string]struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for roomID, worker := range p.workers {
		if _, ok := active[roomID]; ok {
			continue
		}
		worker.cancel()
		delete(p.workers, roomID)
	}

	for roomID := range active {
		if _, ok := p.workers[roomID]; ok {
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		worker := &roomWorker{cancel: cancel}
		p.workers[roomID] = worker
		go p.runRoom(workerCtx, client, roomID)
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

func (p *Provider) runRoom(ctx context.Context, client pb.RoomManagerClient, roomID string) {
	p.store.beginRoomSync(roomID)
	for {
		attemptCtx, cancel := context.WithCancel(ctx)
		err := p.subscribeRoom(attemptCtx, client, roomID)
		cancel()
		if ctx.Err() != nil {
			return
		}
		p.store.beginRoomSync(roomID)
		p.logger.Warn("WorkAdventure room subscription ended", "reason", safeProviderError(err))
		if !sleep(ctx, p.options.ReconnectInterval) {
			return
		}
	}
}

type receiveResult struct {
	message *pb.ServerToAdminClientMessage
	err     error
}

var errIncompleteRoomSync = errors.New("room presence synchronization incomplete")

func (p *Provider) subscribeRoom(ctx context.Context, client pb.RoomManagerClient, roomID string) error {
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
	var receivedEvents atomic.Uint64
	go func() {
		for {
			message, receiveErr := stream.Recv()
			if receiveErr == nil {
				receivedEvents.Add(1)
			}
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
	ready := false
	validated := false
	pending := make(map[string]userEntry)
	commit := func() {
		if ready {
			return
		}
		ready = true
		p.store.replaceRoom(roomID, pending)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if len(results) > 0 {
				validated = false
				resetTimer(timer, p.options.InitialSyncTimeout)
				continue
			}
			generation := receivedEvents.Load()
			expectedUsers, err := p.roomUserCount(ctx, client, roomID)
			if err != nil {
				return fmt.Errorf("refresh room user count: %w", err)
			}
			if receivedEvents.Load() != generation || len(results) > 0 {
				validated = false
				resetTimer(timer, p.options.InitialSyncTimeout)
				continue
			}
			if pendingConnectionCount(pending) != expectedUsers {
				return errIncompleteRoomSync
			}
			if !validated {
				validated = true
				resetTimer(timer, validationQuietPeriod(p.options.InitialSyncTimeout))
				continue
			}
			commit()
		case result := <-results:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) || errors.Is(result.err, context.Canceled) {
					return nil
				}
				return fmt.Errorf("receive room presence: %w", result.err)
			}
			switch event := result.message.GetMessage().(type) {
			case *pb.ServerToAdminClientMessage_UserJoinedRoom:
				if ready {
					p.store.addUser(roomID, event.UserJoinedRoom.GetUuid(), event.UserJoinedRoom.GetName())
					continue
				}
				addPendingUser(pending, event.UserJoinedRoom.GetUuid(), event.UserJoinedRoom.GetName())
				validated = false
				resetTimer(timer, p.options.InitialSyncTimeout)
			case *pb.ServerToAdminClientMessage_UserLeftRoom:
				if ready {
					p.store.removeUser(roomID, event.UserLeftRoom.GetUuid())
				} else {
					removePendingUser(pending, event.UserLeftRoom.GetUuid())
					validated = false
					resetTimer(timer, p.options.InitialSyncTimeout)
				}
			}
		}
	}
}

func (p *Provider) roomUserCount(ctx context.Context, client pb.RoomManagerClient, roomID string) (int, error) {
	requestCtx, cancel := context.WithTimeout(ctx, p.options.RequestTimeout)
	defer cancel()
	rooms, err := client.GetRooms(requestCtx, &emptypb.Empty{})
	if err != nil {
		return 0, err
	}
	for _, room := range rooms.GetRoomDescription() {
		if room.GetRoomId() == roomID {
			return int(room.GetNbUsers()), nil
		}
	}
	return 0, nil
}

func pendingConnectionCount(users map[string]userEntry) int {
	total := 0
	for _, user := range users {
		total += user.count
	}
	return total
}

func addPendingUser(users map[string]userEntry, id, name string) {
	if id == "" {
		return
	}
	entry := users[id]
	entry.count++
	if name != "" {
		entry.name = name
	}
	users[id] = entry
}

func removePendingUser(users map[string]userEntry, id string) {
	entry, ok := users[id]
	if !ok {
		return
	}
	entry.count--
	if entry.count <= 0 {
		delete(users, id)
		return
	}
	users[id] = entry
}

func validationQuietPeriod(syncTimeout time.Duration) time.Duration {
	const maximum = 250 * time.Millisecond
	if syncTimeout < maximum {
		return syncTimeout
	}
	return maximum
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func safeProviderError(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, errIncompleteRoomSync):
		return "incomplete_sync"
	case errors.Is(err, io.EOF):
		return "stream_closed"
	default:
		return "transport_error"
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
