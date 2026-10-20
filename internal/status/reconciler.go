package status

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Arata1202/workadventure-presence-notifier/internal/notifier"
	"github.com/Arata1202/workadventure-presence-notifier/internal/presence"
)

type StateStore interface {
	Load() (State, error)
	Save(State) error
}

const (
	DefaultRecoverySafetyWindow = 5 * time.Minute
	DefaultRecoveryGracePeriod  = 30 * time.Second
)

var ErrPendingRecoveryGrace = errors.New("pending destination post is still within the recovery grace period")

type Options struct {
	Now                  func() time.Time
	RecoverySafetyWindow time.Duration
	RecoveryGracePeriod  time.Duration
}

type Reconciler struct {
	destination          notifier.Destination
	store                StateStore
	state                State
	renderedHash         string
	hasRenderedHash      bool
	now                  func() time.Time
	recoverySafetyWindow time.Duration
	recoveryGracePeriod  time.Duration
}

func NewReconciler(destination notifier.Destination, store StateStore, supplied ...Options) (*Reconciler, error) {
	if len(supplied) > 1 {
		return nil, errors.New("only one reconciler options value is supported")
	}
	options := Options{
		Now:                  time.Now,
		RecoverySafetyWindow: DefaultRecoverySafetyWindow,
		RecoveryGracePeriod:  DefaultRecoveryGracePeriod,
	}
	if len(supplied) == 1 {
		if supplied[0].Now != nil {
			options.Now = supplied[0].Now
		}
		if supplied[0].RecoverySafetyWindow != 0 {
			options.RecoverySafetyWindow = supplied[0].RecoverySafetyWindow
		}
		if supplied[0].RecoveryGracePeriod != 0 {
			options.RecoveryGracePeriod = supplied[0].RecoveryGracePeriod
		}
	}
	if options.RecoverySafetyWindow < 0 || options.RecoveryGracePeriod < 0 {
		return nil, errors.New("recovery timing options must not be negative")
	}
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	if state.Version != stateVersion {
		return nil, fmt.Errorf("unsupported status state version %d", state.Version)
	}
	if err := validateState(state); err != nil {
		return nil, fmt.Errorf("validate persisted status state: %w", err)
	}
	if state.DestinationID != "" && state.DestinationID != destination.ID() {
		return nil, errors.New("persisted destination does not match configured destination")
	}
	return &Reconciler{
		destination: destination, store: store, state: state, now: options.Now,
		recoverySafetyWindow: options.RecoverySafetyWindow,
		recoveryGracePeriod:  options.RecoveryGracePeriod,
	}, nil
}

func (r *Reconciler) Reconcile(ctx context.Context, snapshot presence.Snapshot, force bool) error {
	if snapshot.Count() == 0 {
		return r.reconcileEmpty(ctx)
	}
	text, err := r.destination.Render(snapshot)
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
	if force && r.state.Current != "" {
		if r.hasRenderedHash && r.renderedHash != hash {
			return r.startPending(ctx, text, hash)
		}
		return r.refreshCurrent(ctx, text, hash)
	}
	if !force && r.state.Current != "" && r.hasRenderedHash && r.renderedHash == hash {
		return r.deleteStale(ctx)
	}
	return r.startPending(ctx, text, hash)
}

func (r *Reconciler) refreshCurrent(ctx context.Context, text, currentHash string) error {
	operationID, err := newUUID()
	if err != nil {
		return fmt.Errorf("generate destination refresh operation ID: %w", err)
	}
	err = r.destination.Update(ctx, r.state.Current, text, operationID)
	if errors.Is(err, notifier.ErrMessageNotFound) {
		updated := cloneState(r.state)
		updated.Current = ""
		if len(updated.Stale) == 0 && updated.Pending == nil {
			updated.DestinationID = ""
		}
		if err := r.save(updated, "persist missing current destination status"); err != nil {
			return err
		}
		r.renderedHash = ""
		r.hasRenderedHash = false
		return r.startPending(ctx, text, currentHash)
	}
	if err != nil {
		return err
	}
	r.renderedHash = currentHash
	r.hasRenderedHash = true
	return r.deleteStale(ctx)
}

func (r *Reconciler) startPending(ctx context.Context, text, currentHash string) error {
	operationID, err := newUUID()
	if err != nil {
		return fmt.Errorf("generate destination operation ID: %w", err)
	}
	updated := cloneState(r.state)
	updated.DestinationID = r.destination.ID()
	updated.Pending = &Pending{OperationID: operationID}
	if err := r.store.Save(updated); err != nil {
		persisted, loadErr := r.store.Load()
		if loadErr == nil {
			r.state = persisted
		}
		return errors.Join(fmt.Errorf("persist pending destination post: %w", err), loadErr)
	}
	r.state = updated
	return r.recoverPending(ctx, text, currentHash)
}

func (r *Reconciler) recoverPending(ctx context.Context, text, currentHash string) error {
	pending := r.state.Pending
	if pending == nil {
		return nil
	}
	if pending.AttemptedAt != nil {
		matches, err := r.findPending(ctx, pending)
		if err != nil {
			return fmt.Errorf("find pending destination status: %w", err)
		}
		if len(matches) > 0 {
			return r.acceptMatches(ctx, matches, text, currentHash)
		}
		if err := r.requireRecoveryGraceElapsed(pending.AttemptedAt); err != nil {
			return err
		}
		updated := cloneState(r.state)
		updated.Pending.AttemptedAt = nil
		if err := r.save(updated, "persist cleared destination post attempt"); err != nil {
			return err
		}
	}
	updated := cloneState(r.state)
	attemptedAt := r.now().UTC()
	updated.Pending.AttemptedAt = &attemptedAt
	if err := r.save(updated, "persist attempted destination post"); err != nil {
		return err
	}
	pending = r.state.Pending
	newRef, err := r.destination.Post(ctx, text, pending.OperationID)
	if err != nil {
		return err
	}
	return r.finalizePending(ctx, newRef, nil, currentHash)
}

func (r *Reconciler) acceptMatches(ctx context.Context, matches []notifier.MessageRef, text, currentHash string) error {
	current := matches[0]
	stale := make([]notifier.MessageRef, 0, len(matches)-1)
	for _, match := range matches[1:] {
		stale = appendUnique(stale, match)
	}
	err := r.destination.Update(ctx, current, text, r.state.Pending.OperationID)
	if errors.Is(err, notifier.ErrMessageNotFound) {
		return r.recoverAfterMissingUpdate(ctx, current, text, currentHash)
	}
	if err != nil {
		return err
	}
	return r.finalizePending(ctx, current, stale, currentHash)
}

func (r *Reconciler) recoverAfterMissingUpdate(ctx context.Context, missingRef notifier.MessageRef, text, currentHash string) error {
	pending := r.state.Pending
	matches, err := r.findPending(ctx, pending)
	if err != nil {
		return fmt.Errorf("recheck pending destination status: %w", err)
	}
	filtered := matches[:0]
	for _, match := range matches {
		if match != missingRef {
			filtered = append(filtered, match)
		}
	}
	updated := cloneState(r.state)
	updated.Stale = appendUnique(updated.Stale, missingRef)
	if len(filtered) > 0 {
		if err := r.save(updated, "persist missing destination post recovery"); err != nil {
			return err
		}
		return r.acceptMatches(ctx, filtered, text, currentHash)
	}
	if err := r.save(updated, "persist missing destination post recovery"); err != nil {
		return err
	}
	if err := r.requireRecoveryGraceElapsed(pending.AttemptedAt); err != nil {
		return err
	}
	updated = cloneState(r.state)
	updated.Pending.AttemptedAt = nil
	if err := r.save(updated, "persist cleared missing destination post attempt"); err != nil {
		return err
	}
	return r.recoverPending(ctx, text, currentHash)
}

func (r *Reconciler) finalizePending(ctx context.Context, newRef notifier.MessageRef, stale []notifier.MessageRef, currentHash string) error {
	previous := r.state
	updated := cloneState(previous)
	updated.DestinationID = r.destination.ID()
	updated.Current = newRef
	updated.Pending = nil
	for _, ref := range stale {
		updated.Stale = appendUnique(updated.Stale, ref)
	}
	if previous.Current != "" && previous.Current != newRef {
		updated.Stale = appendUnique(updated.Stale, previous.Current)
	}
	if err := validateState(updated); err != nil {
		return fmt.Errorf("validate new destination status: %w", err)
	}
	if err := r.store.Save(updated); err != nil {
		persisted, loadErr := r.store.Load()
		if loadErr == nil && persisted.Pending == nil && persisted.Current == newRef {
			r.state = persisted
			r.renderedHash = currentHash
			r.hasRenderedHash = true
			return errors.Join(fmt.Errorf("confirm durable new destination status: %w", err), r.deleteStale(ctx))
		}
		return errors.Join(fmt.Errorf("persist new destination status: %w", err), loadErr)
	}
	r.state = updated
	r.renderedHash = currentHash
	r.hasRenderedHash = true
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
	if pending.AttemptedAt == nil {
		updated := cloneState(r.state)
		updated.Pending = nil
		if updated.Current == "" && len(updated.Stale) == 0 {
			updated.DestinationID = ""
		}
		return r.save(updated, "persist canceled destination post")
	}
	matches, err := r.findPending(ctx, pending)
	if err != nil {
		return fmt.Errorf("find pending destination status for deletion: %w", err)
	}
	for _, match := range matches {
		if err := r.destination.Delete(ctx, match); err != nil && !errors.Is(err, notifier.ErrMessageNotFound) {
			return err
		}
	}
	if len(matches) == 0 {
		if err := r.requireRecoveryGraceElapsed(pending.AttemptedAt); err != nil {
			return err
		}
	}
	updated := cloneState(r.state)
	updated.Pending = nil
	if updated.Current == "" && len(updated.Stale) == 0 {
		updated.DestinationID = ""
	}
	return r.save(updated, "persist deleted pending destination status")
}

func (r *Reconciler) deleteAll(ctx context.Context) error {
	var refs []notifier.MessageRef
	if r.state.Current != "" {
		refs = append(refs, r.state.Current)
	}
	refs = append(refs, r.state.Stale...)
	for _, ref := range refs {
		if err := r.deleteAndForget(ctx, ref); err != nil {
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

func (r *Reconciler) deleteAndForget(ctx context.Context, ref notifier.MessageRef) error {
	err := r.destination.Delete(ctx, ref)
	if err != nil && !errors.Is(err, notifier.ErrMessageNotFound) {
		return err
	}
	updated := cloneState(r.state)
	if updated.Current == ref {
		updated.Current = ""
		r.renderedHash = ""
		r.hasRenderedHash = false
	}
	updated.Stale = removeRef(updated.Stale, ref)
	if updated.Current == "" && len(updated.Stale) == 0 && updated.Pending == nil {
		updated.DestinationID = ""
	}
	if err := r.store.Save(updated); err != nil {
		return fmt.Errorf("persist deleted destination status: %w", err)
	}
	r.state = updated
	return nil
}

func (r *Reconciler) findPending(ctx context.Context, pending *Pending) ([]notifier.MessageRef, error) {
	if pending.AttemptedAt == nil {
		return nil, errors.New("pending destination post has not been attempted")
	}
	oldestTime := pending.AttemptedAt.Add(-r.recoverySafetyWindow)
	if oldestTime.Before(time.Unix(0, 0)) {
		oldestTime = time.Unix(0, 0)
	}
	return r.destination.FindByOperation(ctx, pending.OperationID, oldestTime)
}

func (r *Reconciler) requireRecoveryGraceElapsed(attemptedAt *time.Time) error {
	if attemptedAt == nil {
		return errors.New("pending destination post has not been attempted")
	}
	if r.now().Before(attemptedAt.Add(r.recoveryGracePeriod)) {
		return ErrPendingRecoveryGrace
	}
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
	state.Stale = append([]notifier.MessageRef(nil), state.Stale...)
	if state.Pending != nil {
		pending := *state.Pending
		if pending.AttemptedAt != nil {
			attemptedAt := *pending.AttemptedAt
			pending.AttemptedAt = &attemptedAt
		}
		state.Pending = &pending
	}
	return state
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

func appendUnique(values []notifier.MessageRef, value notifier.MessageRef) []notifier.MessageRef {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func removeRef(values []notifier.MessageRef, target notifier.MessageRef) []notifier.MessageRef {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}
