//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func paymentUserLockRefCount(userID int64) (int, bool) {
	paymentUserLocks.Lock()
	defer paymentUserLocks.Unlock()
	entry, ok := paymentUserLocks.entries[userID]
	if !ok {
		return 0, false
	}
	return entry.refs, true
}

func requirePaymentUserLockRefs(t *testing.T, userID int64, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		refs, ok := paymentUserLockRefCount(userID)
		return ok && refs == want
	}, 2*time.Second, 5*time.Millisecond, "payment user %d lock refs never reached %d", userID, want)
}

func requirePaymentUserLockEntryRemoved(t *testing.T, userID int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, ok := paymentUserLockRefCount(userID)
		return !ok
	}, 2*time.Second, 5*time.Millisecond, "payment user %d lock entry was not removed", userID)
}

func holdPaymentUserLock(t *testing.T, userID int64) func() {
	t.Helper()
	release, err := acquirePaymentUserLock(context.Background(), userID)
	require.NoError(t, err)
	t.Cleanup(release)
	return release
}

func TestAcquirePaymentUserLockSerializesSameUserAndAllowsDifferentUsers(t *testing.T) {
	const (
		userID      int64 = 9_000_000_001
		otherUserID int64 = 9_000_000_002
	)
	releaseFirst := holdPaymentUserLock(t, userID)
	sameUserCtx, cancelSameUser := context.WithCancel(context.Background())
	t.Cleanup(cancelSameUser)

	sameUserResult := make(chan error, 1)
	go func() {
		release, err := acquirePaymentUserLock(sameUserCtx, userID)
		if release != nil {
			release()
		}
		sameUserResult <- err
	}()
	requirePaymentUserLockRefs(t, userID, 2)
	select {
	case err := <-sameUserResult:
		t.Fatalf("same user acquired payment lock before release: %v", err)
	default:
	}

	otherCtx, cancelOther := context.WithTimeout(context.Background(), time.Second)
	defer cancelOther()
	releaseOther, err := acquirePaymentUserLock(otherCtx, otherUserID)
	require.NoError(t, err)
	releaseOther()
	requirePaymentUserLockEntryRemoved(t, otherUserID)

	releaseFirst()
	select {
	case err := <-sameUserResult:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("same user did not acquire payment lock after release")
	}
	releaseFirst()
	requirePaymentUserLockEntryRemoved(t, userID)
}

func TestAcquirePaymentUserLockCanceledWaiterReleasesReference(t *testing.T) {
	const userID int64 = 9_000_000_003
	releaseFirst := holdPaymentUserLock(t, userID)
	waitCtx, cancelWait := context.WithCancel(context.Background())
	t.Cleanup(cancelWait)
	waitResult := make(chan error, 1)
	go func() {
		release, err := acquirePaymentUserLock(waitCtx, userID)
		if release != nil {
			release()
		}
		waitResult <- err
	}()
	requirePaymentUserLockRefs(t, userID, 2)

	cancelWait()
	select {
	case err := <-waitResult:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled payment lock waiter did not return")
	}
	requirePaymentUserLockRefs(t, userID, 1)

	releaseFirst()
	releaseFirst()
	requirePaymentUserLockEntryRemoved(t, userID)
}

func TestAcquirePaymentUserLockCanceledContextDoesNotLeakEntry(t *testing.T) {
	const userID int64 = 9_000_000_004
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	release, err := acquirePaymentUserLock(ctx, userID)
	require.Nil(t, release)
	require.ErrorIs(t, err, context.Canceled)
	requirePaymentUserLockEntryRemoved(t, userID)
}

func TestLockPaymentUserRowUsesPostgresForUpdate(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT .*"users"\."id".* FROM "users".*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))

	require.NoError(t, lockPaymentUserRow(ctx, tx, 42))
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}
