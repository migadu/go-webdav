package webdav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var lockTokenRegexp = regexp.MustCompile(`^opaquelocktoken:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// RFC 4918 section 6.5 and appendix C: lock tokens are unique URIs, here
// opaquelocktoken URIs made of random UUIDs.
func TestNewLockToken(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		token := newLockToken()
		if !lockTokenRegexp.MatchString(token) {
			t.Fatalf("newLockToken() = %q, want an opaquelocktoken URI made of a version 4 UUID", token)
		}
		if seen[token] {
			t.Fatalf("newLockToken() returned %q twice", token)
		}
		seen[token] = true
	}
}

// fakeClock is a clock for MemLockSystem.now which only moves when told to.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func lockRoots(locks []Lock) string {
	l := make([]string, len(locks))
	for i, lock := range locks {
		l[i] = lock.Root
	}
	sort.Strings(l)
	return strings.Join(l, " ")
}

// RFC 4918 section 6.1: an exclusive lock conflicts with any other lock whose
// scope overlaps, shared locks conflict only with exclusive ones.
func TestMemLockSystemConflicts(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		existing   Lock
		lock       Lock
		conflicted bool
	}{
		{"same resource", Lock{Root: "/a"}, Lock{Root: "/a"}, true},
		{"same resource, shared then exclusive", Lock{Root: "/a", Shared: true}, Lock{Root: "/a"}, true},
		{"same resource, exclusive then shared", Lock{Root: "/a"}, Lock{Root: "/a", Shared: true}, true},
		{"same resource, both shared", Lock{Root: "/a", Shared: true}, Lock{Root: "/a", Shared: true}, false},
		{"other resource", Lock{Root: "/a"}, Lock{Root: "/b"}, false},
		{"resource sharing a prefix", Lock{Root: "/a", Recursive: true}, Lock{Root: "/ab"}, false},
		{"member of a recursive lock", Lock{Root: "/a", Recursive: true}, Lock{Root: "/a/b/c"}, true},
		{"member of a depth 0 lock", Lock{Root: "/a"}, Lock{Root: "/a/b"}, false},
		{"recursive lock above a lock", Lock{Root: "/a/b/c"}, Lock{Root: "/a", Recursive: true}, true},
		{"depth 0 lock above a lock", Lock{Root: "/a/b"}, Lock{Root: "/a"}, false},
		{"recursive lock on the root", Lock{Root: "/a"}, Lock{Root: "/", Recursive: true}, true},
		{"member of a recursive lock on the root", Lock{Root: "/", Recursive: true}, Lock{Root: "/a"}, true},
		{"shared recursive locks", Lock{Root: "/a", Recursive: true, Shared: true}, Lock{Root: "/a/b", Recursive: true, Shared: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ls := &MemLockSystem{}
			if _, err := ls.Create(ctx, tc.existing); err != nil {
				t.Fatalf("Create(%+v) = %v", tc.existing, err)
			}
			_, err := ls.Create(ctx, tc.lock)
			if tc.conflicted && !errors.Is(err, ErrLocked) {
				t.Errorf("Create(%+v) = %v, want ErrLocked", tc.lock, err)
			} else if !tc.conflicted && err != nil {
				t.Errorf("Create(%+v) = %v, want no error", tc.lock, err)
			}
		})
	}
}

func TestMemLockSystemLocks(t *testing.T) {
	ctx := context.Background()
	ls := &MemLockSystem{}
	for _, lock := range []Lock{
		{Root: "/a", Recursive: true},
		{Root: "/b"},
		{Root: "/b/c"},
		{Root: "/bc"},
		{Root: "/d/e/f", Shared: true},
		{Root: "/d/e/f", Shared: true},
	} {
		created, err := ls.Create(ctx, lock)
		if err != nil {
			t.Fatalf("Create(%+v) = %v", lock, err)
		}
		if !lockTokenRegexp.MatchString(created.Token) || created.Root != lock.Root {
			t.Errorf("Create(%+v) = %+v", lock, created)
		}
	}

	for _, tc := range []struct {
		path    string
		members bool
		want    string
	}{
		{"/", false, ""},
		{"/", true, "/a /b /b/c /bc /d/e/f /d/e/f"},
		{"/a", false, "/a"},
		{"/a/x/y", false, "/a"},
		{"/b", false, "/b"},
		{"/b", true, "/b /b/c"},
		{"/b/x", false, ""},
		{"/b/c", true, "/b/c"},
		{"/d", true, "/d/e/f /d/e/f"},
		{"/d/e/f", false, "/d/e/f /d/e/f"},
	} {
		locks, err := ls.Locks(ctx, tc.path, tc.members)
		if err != nil {
			t.Fatalf("Locks(%q, %v) = %v", tc.path, tc.members, err)
		}
		if got := lockRoots(locks); got != tc.want {
			t.Errorf("Locks(%q, %v) = [%v], want [%v]", tc.path, tc.members, got, tc.want)
		}
	}

	locks, _ := ls.Locks(ctx, "/b", true)
	for _, lock := range locks {
		if err := ls.Unlock(ctx, lock.Token); err != nil {
			t.Errorf("Unlock(%v) = %v", lock.Token, err)
		}
	}
	locks, _ = ls.Locks(ctx, "/", true)
	if got, want := lockRoots(locks), "/a /bc /d/e/f /d/e/f"; got != want {
		t.Errorf("after unlocking /b: locks = [%v], want [%v]", got, want)
	}

	for _, lock := range locks {
		if err := ls.Unlock(ctx, lock.Token); err != nil {
			t.Errorf("Unlock(%v) = %v", lock.Token, err)
		}
	}
	if err := ls.Unlock(ctx, locks[0].Token); !errors.Is(err, ErrNoSuchLock) {
		t.Errorf("Unlock() of a removed lock = %v, want ErrNoSuchLock", err)
	}
	if _, err := ls.Refresh(ctx, locks[0].Token, time.Minute); !errors.Is(err, ErrNoSuchLock) {
		t.Errorf("Refresh() of a removed lock = %v, want ErrNoSuchLock", err)
	}
	if locks, _ := ls.Locks(ctx, "/", true); len(locks) != 0 {
		t.Errorf("after Unlock: locks = %v, want none", locks)
	}
}

// RFC 4918 section 6.6: a lock expires once its timeout has elapsed since
// its creation or last refresh.
func TestMemLockSystemTimeout(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	ls := &MemLockSystem{MaxTimeout: -1, now: clock.now}

	lock, err := ls.Create(ctx, Lock{Root: "/a", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.now().Add(10 * time.Second); lock.Timeout != 10*time.Second || !lock.Expires.Equal(want) {
		t.Errorf("Create() = timeout %v, expires %v, want 10s, %v", lock.Timeout, lock.Expires, want)
	}
	infinite, err := ls.Create(ctx, Lock{Root: "/b"})
	if err != nil {
		t.Fatal(err)
	}
	if infinite.Timeout != 0 || !infinite.Expires.IsZero() {
		t.Errorf("Create() with an infinite timeout = timeout %v, expires %v, want none", infinite.Timeout, infinite.Expires)
	}

	clock.advance(9 * time.Second)
	lock, err = ls.Refresh(ctx, lock.Token, 20*time.Second)
	if err != nil {
		t.Fatalf("Refresh() = %v", err)
	}
	if want := clock.now().Add(20 * time.Second); lock.Timeout != 20*time.Second || !lock.Expires.Equal(want) {
		t.Errorf("Refresh() = timeout %v, expires %v, want 20s, %v", lock.Timeout, lock.Expires, want)
	}

	clock.advance(19 * time.Second)
	if locks, _ := ls.Locks(ctx, "/a", false); len(locks) != 1 {
		t.Errorf("Locks() before expiry = %v, want the lock", locks)
	}
	clock.advance(time.Second)
	if locks, _ := ls.Locks(ctx, "/a", false); len(locks) != 0 {
		t.Errorf("Locks() after expiry = %v, want none", locks)
	}
	if _, err := ls.Refresh(ctx, lock.Token, time.Minute); !errors.Is(err, ErrNoSuchLock) {
		t.Errorf("Refresh() of an expired lock = %v, want ErrNoSuchLock", err)
	}
	if _, err := ls.Create(ctx, Lock{Root: "/a"}); err != nil {
		t.Errorf("Create() over an expired lock = %v", err)
	}

	clock.advance(1000 * time.Hour)
	if locks, _ := ls.Locks(ctx, "/b", false); len(locks) != 1 {
		t.Errorf("Locks() = %v, want the lock with an infinite timeout", locks)
	}
}

func TestMemLockSystemMaxTimeout(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	ls := &MemLockSystem{MaxTimeout: time.Minute, now: clock.now}

	for i, tc := range []struct {
		timeout, want time.Duration
	}{
		{0, time.Minute},
		{time.Hour, time.Minute},
		{30 * time.Second, 30 * time.Second},
	} {
		lock, err := ls.Create(ctx, Lock{Root: fmt.Sprintf("/%d", i), Timeout: tc.timeout})
		if err != nil {
			t.Fatal(err)
		}
		if lock.Timeout != tc.want || !lock.Expires.Equal(clock.now().Add(tc.want)) {
			t.Errorf("Create() with timeout %v = timeout %v, expires %v, want %v", tc.timeout, lock.Timeout, lock.Expires, tc.want)
		}
		lock, err = ls.Refresh(ctx, lock.Token, tc.timeout)
		if err != nil || lock.Timeout != tc.want {
			t.Errorf("Refresh() with timeout %v = timeout %v, %v, want %v", tc.timeout, lock.Timeout, err, tc.want)
		}
	}
}

func TestMemLockSystemConcurrent(t *testing.T) {
	ctx := context.Background()
	ls := &MemLockSystem{}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int
	)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := ls.Create(ctx, Lock{Root: "/a"})
			if errors.Is(err, ErrLocked) {
				return
			} else if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			granted++
			mu.Unlock()
			ls.Locks(ctx, "/", true)
			ls.Refresh(ctx, lock.Token, time.Minute)
		}()
	}
	wg.Wait()
	if granted != 1 {
		t.Errorf("%d exclusive locks granted on the same resource, want 1", granted)
	}
}

// With a LockSystem, the Handler advertises DAV class 2 and the LOCK and
// UNLOCK methods (RFC 4918 sections 10.1 and 18.2), and the supportedlock
// property.
func TestHandlerLockSystemAdvertised(t *testing.T) {
	dir := newLockTestDir(t)
	h := &Handler{FileSystem: LocalFileSystem(dir), LockSystem: &MemLockSystem{}}

	for _, target := range []string{"/file.txt", "/dir/"} {
		w := serveLockTest(h, http.MethodOptions, target, "")
		if classes := davClasses(w.Header()); !hasToken(classes, "1") || !hasToken(classes, "2") {
			t.Errorf("OPTIONS %v: DAV = %v, want classes 1 and 2", target, classes)
		}
		if allow := allowedMethods(w.Header()); !hasToken(allow, "LOCK") || !hasToken(allow, "UNLOCK") {
			t.Errorf("OPTIONS %v: Allow = %v, want LOCK and UNLOCK", target, allow)
		}
	}

	w := serveLockTest(h, "PROPFIND", "/file.txt", "", "Depth", "0")
	if !strings.Contains(w.Body.String(), "<supportedlock") {
		t.Errorf("PROPFIND allprop lacks supportedlock:\n%v", w.Body.String())
	}

	w = serveLockTest(h, "LOCK", "/file.txt", exclusiveLockInfo)
	if w.Code != http.StatusOK {
		t.Fatalf("LOCK: status = %v, want 200", w.Code)
	}
	token := strings.Trim(w.Header().Get("Lock-Token"), "<>")
	if !lockTokenRegexp.MatchString(token) {
		t.Fatalf("LOCK: Lock-Token = %q", w.Header().Get("Lock-Token"))
	}
	if w = serveLockTest(h, http.MethodPut, "/file.txt", ""); w.Code != http.StatusLocked {
		t.Errorf("PUT without the lock token: status = %v, want 423", w.Code)
	}
	if w = serveLockTest(h, http.MethodPut, "/file.txt", "", "If", "(<"+token+">)"); w.Code != http.StatusNoContent {
		t.Errorf("PUT with the lock token: status = %v, want 204", w.Code)
	}
	if w = serveLockTest(h, "UNLOCK", "/file.txt", "", "Lock-Token", "<"+token+">"); w.Code != http.StatusNoContent {
		t.Errorf("UNLOCK: status = %v, want 204", w.Code)
	}
	if w = serveLockTest(h, http.MethodPut, "/file.txt", ""); w.Code != http.StatusNoContent {
		t.Errorf("PUT after UNLOCK: status = %v, want 204", w.Code)
	}
}
