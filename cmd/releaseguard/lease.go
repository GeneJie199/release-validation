package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/GeneJie199/release-validation/internal/runstore"
)

type runLease struct {
	store *runstore.Store
	runID string
	owner string
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
	errMu sync.Mutex
	err   error
}

func acquireRunLease(ctx context.Context, store *runstore.Store, runID string, cancel context.CancelFunc) (*runLease, error) {
	owner, err := runstore.NewLeaseOwner()
	if err != nil {
		return nil, err
	}
	acquired, err := store.AcquireLease(ctx, runID, owner)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("run %s is already executing in another process; inspect it with releaseguard runs", runID)
	}
	lease := &runLease{store: store, runID: runID, owner: owner, stop: make(chan struct{}), done: make(chan struct{})}
	go lease.heartbeat(cancel)
	return lease, nil
}

func (l *runLease) heartbeat(cancel context.CancelFunc) {
	defer close(l.done)
	ticker := time.NewTicker(runstore.LeaseTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			err := l.store.RenewLease(ctx, l.runID, l.owner)
			stop()
			if err != nil {
				l.errMu.Lock()
				l.err = fmt.Errorf("renew run lease: %w", err)
				l.errMu.Unlock()
				cancel()
				return
			}
		}
	}
}

func (l *runLease) close(release bool) error {
	if l == nil {
		return nil
	}
	l.once.Do(func() { close(l.stop) })
	<-l.done
	var releaseErr error
	if release {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr = l.store.ReleaseLease(ctx, l.runID, l.owner)
		cancel()
	}
	l.errMu.Lock()
	defer l.errMu.Unlock()
	return errors.Join(l.err, releaseErr)
}
