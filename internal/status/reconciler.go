package status

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier/slack"
	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

type Slack interface {
	ChannelID() string
	Render(presence.Snapshot) (string, error)
	Post(context.Context, string, string) (string, error)
	Update(context.Context, string, string, string) error
	Delete(context.Context, string) error
	FindByOperation(context.Context, string, string) ([]slack.Message, error)
}

type StateStore interface {
	Load() (State, error)
	Save(State) error
}

type Reconciler struct {
	slack Slack
	store StateStore
	state State
}

func NewReconciler(destination Slack, store StateStore) (*Reconciler, error) {
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	if state.ChannelID != "" && state.ChannelID != destination.ChannelID() {
		return nil, errors.New("persisted Slack channel does not match configured channel")
	}
	if state.Pending != nil && (state.Pending.OperationID == "" || state.Pending.ContentHash == "") {
		return nil, errors.New("persisted pending Slack post is incomplete")
	}
	return &Reconciler{slack: destination, store: store, state: state}, nil
}

func (r *Reconciler) Reconcile(ctx context.Context, snapshot presence.Snapshot, force bool) error {
	if snapshot.Count() == 0 {
		return r.reconcileEmpty(ctx)
	}
	text, err := r.slack.Render(snapshot)
	if err != nil {
		return err
	}
	hash := contentHash(text)
	if r.state.Pending != nil {
		if err := r.recoverPending(ctx, text, hash); err != nil {
			return err
		}
		return nil
	}
	if !force && r.state.MessageTS != "" && r.state.ContentHash == hash {
		return r.deleteStale(ctx)
	}
	operationID, err := newUUID()
	if err != nil {
		return fmt.Errorf("generate Slack operation ID: %w", err)
	}
	updated := cloneState(r.state)
	updated.ChannelID = r.slack.ChannelID()
	updated.Pending = &Pending{OperationID: operationID, ContentHash: hash}
	if err := r.store.Save(updated); err != nil {
		persisted, loadErr := r.store.Load()
		if loadErr == nil {
			r.state = persisted
		}
		return errors.Join(fmt.Errorf("persist pending Slack post: %w", err), loadErr)
	}
	r.state = updated
	return r.recoverPending(ctx, text, hash)
}

func (r *Reconciler) recoverPending(ctx context.Context, text, currentHash string) error {
	pending := r.state.Pending
	if pending == nil {
		return nil
	}
	if pending.AttemptedAt != "" {
		matches, err := r.slack.FindByOperation(ctx, pending.OperationID, pending.AttemptedAt)
		if err != nil {
			return fmt.Errorf("find pending Slack status: %w", err)
		}
		if len(matches) > 0 {
			return r.acceptMatches(ctx, matches, text, currentHash)
		}
		updated := cloneState(r.state)
		updated.Pending.AttemptedAt = ""
		if err := r.save(updated, "persist cleared Slack post attempt"); err != nil {
			return err
		}
	}
	updated := cloneState(r.state)
	updated.Pending.AttemptedAt = slackTimestamp(time.Now())
	if err := r.save(updated, "persist attempted Slack post"); err != nil {
		return err
	}
	pending = r.state.Pending
	newTimestamp, err := r.slack.Post(ctx, text, pending.OperationID)
	if err != nil {
		return err
	}
	return r.finalizePending(ctx, newTimestamp, nil, currentHash)
}

func (r *Reconciler) acceptMatches(ctx context.Context, matches []slack.Message, text, currentHash string) error {
	sort.Slice(matches, func(i, j int) bool { return matches[i].Timestamp > matches[j].Timestamp })
	current := matches[0].Timestamp
	stale := make([]string, 0, len(matches)-1)
	for _, match := range matches[1:] {
		stale = appendUnique(stale, match.Timestamp)
	}
	if r.state.Pending.ContentHash != currentHash {
		err := r.slack.Update(ctx, current, text, r.state.Pending.OperationID)
		if errors.Is(err, slack.ErrMessageNotFound) {
			return r.recoverAfterMissingUpdate(ctx, current, text, currentHash)
		}
		if err != nil {
			return err
		}
	}
	return r.finalizePending(ctx, current, stale, currentHash)
}

func (r *Reconciler) recoverAfterMissingUpdate(ctx context.Context, missingTimestamp, text, currentHash string) error {
	pending := r.state.Pending
	matches, err := r.slack.FindByOperation(ctx, pending.OperationID, pending.AttemptedAt)
	if err != nil {
		return fmt.Errorf("recheck pending Slack status: %w", err)
	}
	filtered := matches[:0]
	for _, match := range matches {
		if match.Timestamp != missingTimestamp {
			filtered = append(filtered, match)
		}
	}
	updated := cloneState(r.state)
	updated.Stale = appendUnique(updated.Stale, missingTimestamp)
	if len(filtered) > 0 {
		if err := r.save(updated, "persist missing Slack post recovery"); err != nil {
			return err
		}
		return r.acceptMatches(ctx, filtered, text, currentHash)
	}
	updated.Pending.AttemptedAt = ""
	if err := r.save(updated, "persist missing Slack post recovery"); err != nil {
		return err
	}
	return r.recoverPending(ctx, text, currentHash)
}

func (r *Reconciler) finalizePending(ctx context.Context, newTimestamp string, stale []string, currentHash string) error {
	previous := r.state
	updated := cloneState(previous)
	updated.ChannelID = r.slack.ChannelID()
	updated.MessageTS = newTimestamp
	updated.ContentHash = currentHash
	updated.Pending = nil
	for _, timestamp := range stale {
		updated.Stale = appendUnique(updated.Stale, timestamp)
	}
	if previous.MessageTS != "" && previous.MessageTS != newTimestamp {
		updated.Stale = appendUnique(updated.Stale, previous.MessageTS)
	}
	if err := r.store.Save(updated); err != nil {
		persisted, loadErr := r.store.Load()
		if loadErr == nil && persisted.Pending == nil && persisted.MessageTS == newTimestamp {
			r.state = persisted
			return errors.Join(fmt.Errorf("confirm durable new Slack status: %w", err), r.deleteStale(ctx))
		}
		return errors.Join(fmt.Errorf("persist new Slack status: %w", err), loadErr)
	}
	r.state = updated
	return r.deleteStale(ctx)
}

func (r *Reconciler) DeleteAll(ctx context.Context) error { return r.reconcileEmpty(ctx) }

func (r *Reconciler) reconcileEmpty(ctx context.Context) error {
	if err := r.deleteAll(ctx); err != nil {
		return err
	}
	pending := r.state.Pending
	if pending == nil {
		return nil
	}
	if pending.AttemptedAt == "" {
		updated := cloneState(r.state)
		updated.Pending = nil
		if updated.MessageTS == "" && len(updated.Stale) == 0 {
			updated.ChannelID = ""
		}
		return r.save(updated, "persist canceled Slack post")
	}
	matches, err := r.slack.FindByOperation(ctx, pending.OperationID, pending.AttemptedAt)
	if err != nil {
		return fmt.Errorf("find pending Slack status for deletion: %w", err)
	}
	for _, match := range matches {
		if err := r.slack.Delete(ctx, match.Timestamp); err != nil && !errors.Is(err, slack.ErrMessageNotFound) {
			return err
		}
	}
	updated := cloneState(r.state)
	updated.Pending = nil
	if updated.MessageTS == "" && len(updated.Stale) == 0 {
		updated.ChannelID = ""
	}
	return r.save(updated, "persist deleted pending Slack status")
}

func (r *Reconciler) deleteAll(ctx context.Context) error {
	var timestamps []string
	if r.state.MessageTS != "" {
		timestamps = append(timestamps, r.state.MessageTS)
	}
	timestamps = append(timestamps, r.state.Stale...)
	for _, timestamp := range timestamps {
		if err := r.deleteAndForget(ctx, timestamp); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) deleteStale(ctx context.Context) error {
	for len(r.state.Stale) > 0 {
		if err := r.deleteAndForget(ctx, r.state.Stale[0]); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) deleteAndForget(ctx context.Context, timestamp string) error {
	err := r.slack.Delete(ctx, timestamp)
	if err != nil && !errors.Is(err, slack.ErrMessageNotFound) {
		return err
	}
	updated := cloneState(r.state)
	if updated.MessageTS == timestamp {
		updated.MessageTS = ""
		updated.ContentHash = ""
	}
	updated.Stale = removeTimestamp(updated.Stale, timestamp)
	if updated.MessageTS == "" && len(updated.Stale) == 0 && updated.Pending == nil {
		updated.ChannelID = ""
	}
	if err := r.store.Save(updated); err != nil {
		return fmt.Errorf("persist deleted Slack status: %w", err)
	}
	r.state = updated
	return nil
}

func (r *Reconciler) save(state State, action string) error {
	if err := r.store.Save(state); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	r.state = state
	return nil
}

func cloneState(state State) State {
	state.Stale = append([]string(nil), state.Stale...)
	if state.Pending != nil {
		pending := *state.Pending
		state.Pending = &pending
	}
	return state
}

func slackTimestamp(value time.Time) string {
	return fmt.Sprintf("%d.%06d", value.Unix(), value.Nanosecond()/1000)
}

func contentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func removeTimestamp(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}
