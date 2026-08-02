package service

import (
	"context"
	"fmt"
	"sync"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
)

type paymentUserLockEntry struct {
	token chan struct{}
	refs  int
}

var paymentUserLocks = struct {
	sync.Mutex
	entries map[int64]*paymentUserLockEntry
}{
	entries: make(map[int64]*paymentUserLockEntry),
}

func acquirePaymentUserLock(ctx context.Context, userID int64) (func(), error) {
	paymentUserLocks.Lock()
	entry := paymentUserLocks.entries[userID]
	if entry == nil {
		entry = &paymentUserLockEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		paymentUserLocks.entries[userID] = entry
	}
	entry.refs++
	paymentUserLocks.Unlock()

	select {
	case <-ctx.Done():
		releasePaymentUserLockRef(userID, entry)
		return nil, fmt.Errorf("acquire payment user lock: %w", ctx.Err())
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			releasePaymentUserLockRef(userID, entry)
			return nil, fmt.Errorf("acquire payment user lock: %w", err)
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.token <- struct{}{}
			releasePaymentUserLockRef(userID, entry)
		})
	}, nil
}

func releasePaymentUserLockRef(userID int64, entry *paymentUserLockEntry) {
	paymentUserLocks.Lock()
	defer paymentUserLocks.Unlock()
	entry.refs--
	if entry.refs == 0 && paymentUserLocks.entries[userID] == entry {
		delete(paymentUserLocks.entries, userID)
	}
}

func lockPaymentUserRow(ctx context.Context, tx *dbent.Tx, userID int64) error {
	if tx == nil {
		return fmt.Errorf("lock payment user row: nil transaction")
	}
	_, err := tx.User.Query().
		Where(
			dbuser.IDEQ(userID),
			func(selector *entsql.Selector) {
				if selector.Dialect() == dialect.Postgres {
					selector.ForUpdate()
				}
			},
		).
		OnlyID(ctx)
	if err != nil {
		return fmt.Errorf("lock payment user row: %w", err)
	}
	return nil
}
