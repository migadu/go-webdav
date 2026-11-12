package webdav

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrLocked is returned by LockSystem.Create when the new lock conflicts
	// with an existing one.
	ErrLocked = errors.New("webdav: conflicting lock")
	// ErrNoSuchLock is returned by LockSystem.Refresh and LockSystem.Unlock
	// when there is no active lock with the given token.
	ErrNoSuchLock = errors.New("webdav: no such lock")
)

// Lock is a WebDAV write lock (RFC 4918 section 6).
type Lock struct {
	// Token is the lock token, a unique URI identifying the lock.
	Token string
	// Root is the path of the locked resource, the root of the lock.
	Root string
	// Recursive is set for a lock created with "Depth: infinity", which
	// locks a collection and all of its members, unset for a lock created
	// with "Depth: 0", which locks only the resource at Root and, for a
	// collection, the set of its members (RFC 4918 section 7.4).
	Recursive bool
	// Shared is set for a shared lock, unset for an exclusive one (RFC 4918
	// section 6.1).
	Shared bool
	// Owner is the DAV:owner XML element supplied by the client which
	// created the lock, if any. It is returned as is in the DAV:lockdiscovery
	// property.
	Owner string
	// Timeout is the lifetime of the lock, from its creation or last
	// refresh. Zero means infinite.
	Timeout time.Duration
	// Expires is the time when the lock expires. The zero time means never.
	Expires time.Time
}

// inScope reports whether the resource at p, a clean path, is in the scope of
// the lock: whether it's the root of the lock, or a member of the root of a
// recursive lock.
func (lock *Lock) inScope(p string) bool {
	return lock.Root == p || (lock.Recursive && isPathBelow(p, lock.Root))
}

// overlaps reports whether the scopes of two locks overlap.
func (lock *Lock) overlaps(other *Lock) bool {
	return lock.inScope(other.Root) || other.inScope(lock.Root)
}

// conflicts reports whether two locks can't be both active: whether their
// scopes overlap and at least one of them is exclusive (RFC 4918 section
// 6.1).
func (lock *Lock) conflicts(other *Lock) bool {
	if lock.Shared && other.Shared {
		return false
	}
	return lock.overlaps(other)
}

// isPathBelow reports whether the clean path p is below dir.
func isPathBelow(p, dir string) bool {
	if dir == "/" {
		return p != "/" && strings.HasPrefix(p, "/")
	}
	return strings.HasPrefix(p, dir+"/")
}

// LockSystem stores the locks of a Handler. Setting Handler.LockSystem
// enables WebDAV locking (RFC 4918 compliance class 2).
//
// MemLockSystem stores locks in memory. Deployments running several servers
// in front of the same FileSystem need a LockSystem shared by these servers.
//
// Paths are absolute slash-separated paths, cleaned as with path.Clean: they
// don't end with a slash, except for "/". A lock's scope is the resource at
// its root and, for a recursive lock, all of its members, that is all the
// paths below the root (RFC 4918 section 7.4).
//
// Locks are identified by the paths of the resources: a LockSystem holds the
// locks of a single tree of resources. Handlers serving different trees at
// the same paths, such as a FileSystem per user, need a LockSystem each, or
// one telling the trees apart, for instance from the request context.
//
// Locks coordinate the clients writing resources, they don't restrict who
// writes them. The Handler doesn't check that a lock token is submitted by
// the principal which created the lock (RFC 4918 section 6.4): a client
// which can read the lockdiscovery property can use the tokens listed there.
// The Handler checks the locks before carrying out a request, not
// atomically with it: a lock created meanwhile doesn't stop a request which
// was already checked.
//
// The methods of a LockSystem may be called concurrently.
type LockSystem interface {
	// Create creates a lock. It ignores lock.Token and lock.Expires,
	// generates a new unique token and sets the expiry time from
	// lock.Timeout, which it may shorten. It returns the created lock.
	//
	// If the new lock conflicts with an active lock, Create returns an error
	// wrapping ErrLocked. Two locks conflict if the scope of one includes
	// the root of the other, unless both are shared (RFC 4918 section 6.1).
	// Create may refuse a lock for other reasons, such as a limit on the
	// number of locks: the Handler replies with the status of an error
	// created with NewHTTPError, such as 507 Insufficient Storage.
	Create(ctx context.Context, lock Lock) (Lock, error)
	// Refresh restarts the lifetime of the lock with the given token, and
	// sets its timeout to timeout, which it may shorten. It returns the
	// refreshed lock, or an error wrapping ErrNoSuchLock if there is no
	// such active lock.
	Refresh(ctx context.Context, token string, timeout time.Duration) (Lock, error)
	// Unlock removes the lock with the given token. It returns an error
	// wrapping ErrNoSuchLock if there is no such active lock.
	Unlock(ctx context.Context, token string) error
	// Locks returns the active locks whose scope includes the resource at
	// path, and, if members is set, the active locks rooted below path.
	Locks(ctx context.Context, path string, members bool) ([]Lock, error)
}

// MemLockSystem is a LockSystem keeping locks in memory, for a server
// running on its own. Its zero value is ready to use.
//
// Expired locks are removed by the next call to any method. Its methods take
// a time proportional to the number of locks, which MaxLocks limits.
//
// Create refuses a lock over the limits with a 507 Insufficient Storage
// error (RFC 4918 section 11.5): the server can't record more locks until
// some are removed.
type MemLockSystem struct {
	// MaxTimeout is the longest lifetime given to a lock, including one
	// requested with an infinite timeout. Zero means an hour. A negative
	// value means no limit: a lock requested without timeout or with an
	// infinite one, by a client which then goes away, is then only removed
	// when the process exits.
	MaxTimeout time.Duration
	// MaxLocks is the maximum number of active locks. Zero means 10000. A
	// negative value means no limit.
	MaxLocks int
	// MaxSharedLocks is the maximum number of shared locks whose scopes
	// overlap: Create refuses a shared lock whose scope overlaps those of
	// MaxSharedLocks active locks. It limits the number of locks on a
	// resource, all listed in its lockdiscovery property, to MaxSharedLocks.
	// A depth-infinity lock on a collection counts the locks of all its
	// members, even if each member has fewer. Zero means 64. A negative
	// value means no limit.
	MaxSharedLocks int
	// MaxOwnerBytes is the maximum total size, in bytes, of the Owner of
	// locks whose scopes overlap: Create refuses a lock if its Owner and
	// those of the active locks overlapping its scope add up to more. It
	// limits the size of the owners listed in the lockdiscovery property of
	// a resource, counted as for MaxSharedLocks. Zero means 64 KiB. A
	// negative value means no limit.
	MaxOwnerBytes int

	mu    sync.Mutex
	locks map[string]Lock  // by token
	now   func() time.Time // replaced by tests
}

var _ LockSystem = (*MemLockSystem)(nil)

// The default limits of MemLockSystem.
const (
	// An hour bounds how long a lock left behind by a client which went
	// away blocks the others, and is long enough for clients to refresh
	// their locks now and then: the timeout is in the LOCK response.
	defaultMaxLockTimeout = time.Hour
	// Ten thousand locks, each with a DAV:owner element of at most 4 KiB,
	// take about 45 MB, and listing the locks of a resource among them
	// takes a fraction of a millisecond.
	defaultMaxLocks = 10000
	// Clients rarely share a lock with more than a few others.
	defaultMaxSharedLocks = 64
	// Owners are usually a name or a URL, so this only stops large owners
	// adding up: 64 shared locks with owners of 4 KiB, the most a Handler
	// accepts, would add 256 KiB to every resource in their scope in a
	// PROPFIND response. 64 KiB is 16 such owners, or 64 of 1 KiB.
	defaultMaxOwnerBytes = 64 << 10
)

// limit returns the value of a limit field: def if v is zero, and zero,
// meaning none, if v is negative.
func limit(v, def int64) int64 {
	switch {
	case v == 0:
		return def
	case v < 0:
		return 0
	default:
		return v
	}
}

func (ls *MemLockSystem) maxLocks() int {
	return int(limit(int64(ls.MaxLocks), defaultMaxLocks))
}

func (ls *MemLockSystem) maxSharedLocks() int {
	return int(limit(int64(ls.MaxSharedLocks), defaultMaxSharedLocks))
}

func (ls *MemLockSystem) maxOwnerBytes() int {
	return int(limit(int64(ls.MaxOwnerBytes), defaultMaxOwnerBytes))
}

func (ls *MemLockSystem) maxTimeout() time.Duration {
	return time.Duration(limit(int64(ls.MaxTimeout), int64(defaultMaxLockTimeout)))
}

// newLockToken generates a lock token: an opaquelocktoken URI (RFC 4918
// appendix C) made of a random (version 4) UUID (RFC 9562 section 5.4).
func newLockToken() string {
	var b [16]byte
	// Since Go 1.24, crypto/rand.Read never returns an error
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 9562 variant
	return fmt.Sprintf("opaquelocktoken:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// expire removes the expired locks and returns the current time. The caller
// must hold ls.mu.
func (ls *MemLockSystem) expire() time.Time {
	now := time.Now()
	if ls.now != nil {
		now = ls.now()
	}
	for token, lock := range ls.locks {
		if !lock.Expires.IsZero() && !now.Before(lock.Expires) {
			delete(ls.locks, token)
		}
	}
	return now
}

// setTimeout sets the timeout and expiry time of a lock (re)started at now.
func (ls *MemLockSystem) setTimeout(lock *Lock, timeout time.Duration, now time.Time) {
	if timeout < 0 {
		timeout = 0
	}
	if max := ls.maxTimeout(); max > 0 && (timeout == 0 || timeout > max) {
		timeout = max
	}
	lock.Timeout = timeout
	lock.Expires = time.Time{}
	if timeout > 0 {
		lock.Expires = now.Add(timeout)
	}
}

// Create implements LockSystem.
func (ls *MemLockSystem) Create(ctx context.Context, lock Lock) (Lock, error) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	now := ls.expire()
	overlapping, ownerBytes := 0, len(lock.Owner)
	for _, other := range ls.locks {
		if lock.conflicts(&other) {
			return Lock{}, fmt.Errorf("%w: %v is locked", ErrLocked, other.Root)
		} else if lock.overlaps(&other) {
			overlapping++
			ownerBytes += len(other.Owner)
		}
	}
	if max := ls.maxLocks(); max > 0 && len(ls.locks) >= max {
		return Lock{}, NewHTTPError(http.StatusInsufficientStorage, fmt.Errorf("webdav: too many locks (%v)", max))
	}
	if max := ls.maxSharedLocks(); max > 0 && overlapping >= max {
		return Lock{}, NewHTTPError(http.StatusInsufficientStorage, fmt.Errorf("webdav: too many shared locks on %v (%v)", lock.Root, max))
	}
	if max := ls.maxOwnerBytes(); max > 0 && ownerBytes > max {
		return Lock{}, NewHTTPError(http.StatusInsufficientStorage, fmt.Errorf("webdav: owners of the locks on %v too large (over %v bytes)", lock.Root, max))
	}

	if ls.locks == nil {
		ls.locks = make(map[string]Lock)
	}
	lock.Token = newLockToken()
	ls.setTimeout(&lock, lock.Timeout, now)
	ls.locks[lock.Token] = lock
	return lock, nil
}

// Refresh implements LockSystem.
func (ls *MemLockSystem) Refresh(ctx context.Context, token string, timeout time.Duration) (Lock, error) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	now := ls.expire()
	lock, ok := ls.locks[token]
	if !ok {
		return Lock{}, ErrNoSuchLock
	}
	ls.setTimeout(&lock, timeout, now)
	ls.locks[token] = lock
	return lock, nil
}

// Unlock implements LockSystem.
func (ls *MemLockSystem) Unlock(ctx context.Context, token string) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	ls.expire()
	if _, ok := ls.locks[token]; !ok {
		return ErrNoSuchLock
	}
	delete(ls.locks, token)
	return nil
}

// Locks implements LockSystem.
func (ls *MemLockSystem) Locks(ctx context.Context, path string, members bool) ([]Lock, error) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	ls.expire()
	var l []Lock
	for _, lock := range ls.locks {
		if lock.inScope(path) || (members && isPathBelow(lock.Root, path)) {
			l = append(l, lock)
		}
	}
	sort.Slice(l, func(i, j int) bool {
		if l[i].Root != l[j].Root {
			return l[i].Root < l[j].Root
		}
		return l[i].Token < l[j].Token
	})
	return l, nil
}
