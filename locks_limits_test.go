package webdav

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// expectInsufficientStorage checks that err is a 507 Insufficient Storage
// HTTPError.
func expectInsufficientStorage(t *testing.T, err error, what string) {
	t.Helper()
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusInsufficientStorage {
		t.Errorf("%v = %v, want a 507 Insufficient Storage HTTPError", what, err)
	} else if errors.Is(err, ErrLocked) {
		t.Errorf("%v = %v, want no ErrLocked", what, err)
	}
}

// MemLockSystem limits the number of active locks.
func TestMemLockSystemMaxLocks(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	ls := &MemLockSystem{MaxLocks: 3, now: clock.now}

	var tokens []string
	for _, root := range []string{"/a", "/b", "/c"} {
		lock, err := ls.Create(ctx, Lock{Root: root, Timeout: time.Minute})
		if err != nil {
			t.Fatalf("Create(%v) = %v", root, err)
		}
		tokens = append(tokens, lock.Token)
	}
	_, err := ls.Create(ctx, Lock{Root: "/d"})
	expectInsufficientStorage(t, err, "Create() over MaxLocks")

	// Unlocked and expired locks make room
	if err := ls.Unlock(ctx, tokens[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := ls.Create(ctx, Lock{Root: "/d"}); err != nil {
		t.Errorf("Create() after Unlock = %v", err)
	}
	clock.advance(time.Minute)
	if _, err := ls.Create(ctx, Lock{Root: "/e"}); err != nil {
		t.Errorf("Create() after locks expired = %v", err)
	}

	// A conflict is still reported as such
	ls = &MemLockSystem{MaxLocks: 1}
	if _, err := ls.Create(ctx, Lock{Root: "/a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ls.Create(ctx, Lock{Root: "/a"}); !errors.Is(err, ErrLocked) {
		t.Errorf("Create() of a conflicting lock over MaxLocks = %v, want ErrLocked", err)
	}
}

// MemLockSystem limits the number of shared locks whose scopes overlap, and
// so the number of locks on any resource.
func TestMemLockSystemMaxSharedLocks(t *testing.T) {
	ctx := context.Background()
	ls := &MemLockSystem{MaxSharedLocks: 2}

	for i := 0; i < 2; i++ {
		if _, err := ls.Create(ctx, Lock{Root: "/a/b", Shared: true}); err != nil {
			t.Fatalf("Create() of shared lock %v = %v", i, err)
		}
	}
	_, err := ls.Create(ctx, Lock{Root: "/a/b", Shared: true})
	expectInsufficientStorage(t, err, "Create() of a third shared lock")
	_, err = ls.Create(ctx, Lock{Root: "/a", Recursive: true, Shared: true})
	expectInsufficientStorage(t, err, "Create() of a shared lock on the collection")

	// Other resources aren't affected
	for _, lock := range []Lock{
		{Root: "/a/c", Shared: true},
		{Root: "/a/c", Shared: true},
		{Root: "/a", Shared: true},
		{Root: "/d"},
	} {
		if _, err := ls.Create(ctx, lock); err != nil {
			t.Errorf("Create(%+v) = %v", lock, err)
		}
	}
	if _, err := ls.Create(ctx, Lock{Root: "/a/b"}); !errors.Is(err, ErrLocked) {
		t.Errorf("Create() of an exclusive lock = %v, want ErrLocked", err)
	}
}

// MemLockSystem limits the total size of the owners of locks whose scopes
// overlap, and so the size of the owners listed in the lockdiscovery of any
// resource.
func TestMemLockSystemMaxOwnerBytes(t *testing.T) {
	ctx := context.Background()
	ls := &MemLockSystem{MaxOwnerBytes: 10}

	for _, owner := range []string{"123456", "1234", ""} {
		if _, err := ls.Create(ctx, Lock{Root: "/a/b", Shared: true, Owner: owner}); err != nil {
			t.Fatalf("Create() of a shared lock with owner %q = %v", owner, err)
		}
	}
	_, err := ls.Create(ctx, Lock{Root: "/a/b", Shared: true, Owner: "1"})
	expectInsufficientStorage(t, err, "Create() of a shared lock over MaxOwnerBytes")
	_, err = ls.Create(ctx, Lock{Root: "/a", Recursive: true, Shared: true, Owner: "1"})
	expectInsufficientStorage(t, err, "Create() of a shared lock on the collection over MaxOwnerBytes")
	_, err = ls.Create(ctx, Lock{Root: "/d", Owner: "12345678901"})
	expectInsufficientStorage(t, err, "Create() of a lock with an owner over MaxOwnerBytes")

	// Other resources aren't affected
	for _, lock := range []Lock{
		{Root: "/a/c", Shared: true, Owner: "1234567890"},
		{Root: "/a", Shared: true, Owner: "1234567890"},
		{Root: "/d", Owner: "1234567890"},
	} {
		if _, err := ls.Create(ctx, lock); err != nil {
			t.Errorf("Create(%+v) = %v", lock, err)
		}
	}
	if _, err := ls.Create(ctx, Lock{Root: "/a/b", Owner: "1"}); !errors.Is(err, ErrLocked) {
		t.Errorf("Create() of an exclusive lock = %v, want ErrLocked", err)
	}
}

// The limits of MemLockSystem have defaults, and negative values remove
// them.
func TestMemLockSystemDefaultLimits(t *testing.T) {
	ls := &MemLockSystem{}
	if got := ls.maxLocks(); got != 10000 {
		t.Errorf("default MaxLocks = %v, want 10000", got)
	}
	if got := ls.maxSharedLocks(); got != 64 {
		t.Errorf("default MaxSharedLocks = %v, want 64", got)
	}
	if got := ls.maxOwnerBytes(); got != 64<<10 {
		t.Errorf("default MaxOwnerBytes = %v, want 64 KiB", got)
	}
	ls = &MemLockSystem{MaxLocks: -1, MaxSharedLocks: -1, MaxOwnerBytes: -1}
	if got := ls.maxLocks(); got != 0 {
		t.Errorf("MaxLocks -1 = limit %v, want none", got)
	}
	if got := ls.maxSharedLocks(); got != 0 {
		t.Errorf("MaxSharedLocks -1 = limit %v, want none", got)
	}
	if got := ls.maxOwnerBytes(); got != 0 {
		t.Errorf("MaxOwnerBytes -1 = limit %v, want none", got)
	}

	ctx := context.Background()
	clock := newFakeClock()
	ls = &MemLockSystem{now: clock.now}
	for _, timeout := range []time.Duration{0, 2 * time.Hour} {
		lock, err := ls.Create(ctx, Lock{Root: "/a", Shared: true, Timeout: timeout})
		if err != nil {
			t.Fatal(err)
		}
		if lock.Timeout != time.Hour || !lock.Expires.Equal(clock.now().Add(time.Hour)) {
			t.Errorf("Create() with timeout %v = timeout %v, expires %v, want an hour", timeout, lock.Timeout, lock.Expires)
		}
		lock, err = ls.Refresh(ctx, lock.Token, timeout)
		if err != nil || lock.Timeout != time.Hour {
			t.Errorf("Refresh() with timeout %v = timeout %v, %v, want an hour", timeout, lock.Timeout, err)
		}
	}
	ls = &MemLockSystem{MaxTimeout: -1}
	if lock, err := ls.Create(ctx, Lock{Root: "/a"}); err != nil || lock.Timeout != 0 || !lock.Expires.IsZero() {
		t.Errorf("Create() without MaxTimeout = timeout %v, expires %v, %v, want an infinite lock", lock.Timeout, lock.Expires, err)
	}
}

// A LOCK request refused because of a limit of the lock system gets its
// status, and doesn't create the resource.
func TestLockLimits(t *testing.T) {
	lt := newLockTest(t)
	lt.ls.MaxLocks = 2
	lt.ls.MaxSharedLocks = 1

	lt.lock(http.StatusOK, "/file.txt", sharedLockInfo)
	lt.expect(http.StatusInsufficientStorage, "LOCK", "/file.txt", sharedLockInfo)
	lt.lock(http.StatusOK, "/dir/a.txt", exclusiveLockInfo)
	lt.expect(http.StatusInsufficientStorage, "LOCK", "/dir/", exclusiveLockInfo, "Depth", "0")
	lt.expect(http.StatusInsufficientStorage, "LOCK", "/new.txt", exclusiveLockInfo)
	if _, err := os.Stat(filepath.Join(lt.dir, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("LOCK over the limit created the resource (stat: %v)", err)
	}
}
