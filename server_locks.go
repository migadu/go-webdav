package webdav

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-webdav/internal"
)

// lockingBackend is the backend of a Handler with a LockSystem: it supports
// LOCK and UNLOCK requests.
type lockingBackend struct {
	*backend
}

var _ internal.LockBackend = lockingBackend{}

// davPrecondition is a precondition or postcondition element of a DAV:error
// element (RFC 4918 section 16).
type davPrecondition struct {
	XMLName xml.Name
	Hrefs   []internal.Href `xml:"href"`
}

// newPreconditionError returns an error with the given HTTP status code and
// DAV:error precondition element, listing hrefs.
func newPreconditionError(code int, name string, hrefs ...string) error {
	elt := &davPrecondition{XMLName: xml.Name{Space: internal.Namespace, Local: name}}
	for _, href := range hrefs {
		elt.Hrefs = append(elt.Hrefs, internal.Href{Path: href})
	}
	raw, err := internal.EncodeRawXMLElement(elt)
	if err != nil {
		return err
	}
	return &internal.HTTPError{Code: code, Err: &internal.Error{Raw: []internal.RawXMLValue{*raw}}}
}

// collectionHref returns the href of the collection at the clean path p.
func collectionHref(p string) string {
	if p == "/" {
		return p
	}
	return p + "/"
}

// evaluateIf evaluates the If header field of a request (RFC 4918 section
// 10.4), and records the lock tokens it submits: all those appearing in it
// (section 10.4.1).
func (b *backend) evaluateIf(r *http.Request) error {
	// An empty If header field has no lists
	hif := strings.TrimSpace(strings.Join(r.Header.Values("If"), " "))
	if hif == "" {
		return nil
	}
	lists, err := internal.ParseConditions(hif)
	if err != nil {
		return &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
	}

	b.submitted = make(map[string]bool)
	for _, l := range lists {
		for _, cond := range l {
			if cond.Token != "" {
				b.submitted[cond.Token] = true
			}
		}
	}

	if r.Method == "LOCK" && internal.IsRequestBodyEmpty(r) {
		// The If header field of a lock refresh names the lock to refresh,
		// which Lock checks
		return nil
	}

	ev := ifEvaluator{b: b, r: r}
	for _, l := range lists {
		if ok, err := ev.list(l); err != nil {
			return err
		} else if ok {
			return nil
		}
	}
	return internal.HTTPErrorf(http.StatusPreconditionFailed, "webdav: the conditions of the If header field aren't met")
}

// ifEvaluator evaluates the lists of an If header field, caching the state
// of the resources they apply to.
type ifEvaluator struct {
	b     *backend
	r     *http.Request
	locks map[string][]Lock
	etags map[string]string
}

// list reports whether all the conditions of a list are met.
func (ev *ifEvaluator) list(l []internal.Condition) (bool, error) {
	for _, cond := range l {
		ok, err := ev.condition(&cond)
		if err != nil || ok == cond.Not {
			return false, err
		}
	}
	return true, nil
}

// condition reports whether a condition, ignoring Not, is met.
func (ev *ifEvaluator) condition(cond *internal.Condition) (bool, error) {
	p := ev.r.URL.Path
	if cond.Resource != "" {
		u, err := url.Parse(cond.Resource)
		if err != nil {
			return false, &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
		}
		p = u.Path
		if p == "" {
			p = "/"
		}
	}
	p = path.Clean(p)

	if cond.Token != "" {
		// A lock token matches the resources in the scope of the lock
		// (RFC 4918 section 10.4.4)
		locks, ok := ev.locks[p]
		if !ok {
			var err error
			locks, err = ev.b.locks.Locks(ev.r.Context(), p, false)
			if err != nil {
				return false, err
			}
			if ev.locks == nil {
				ev.locks = make(map[string][]Lock)
			}
			ev.locks[p] = locks
		}
		for _, lock := range locks {
			if lock.Token == cond.Token {
				return true, nil
			}
		}
		return false, nil
	}

	etag, ok := ev.etags[p]
	if !ok {
		fi, err := ev.b.FileSystem.Stat(ev.r.Context(), p)
		if err == nil {
			etag = fi.ETag
		} else if !internal.IsNotFound(err) {
			return false, err
		}
		// An unmapped URL has no entity-tag
		if ev.etags == nil {
			ev.etags = make(map[string]string)
		}
		ev.etags[p] = etag
	}
	// RFC 4918 section 10.4.4 allows either comparison function
	match, err := ConditionalMatch(cond.ETag).MatchETagWeak(etag)
	return match && err == nil, nil
}

// lockTarget is a resource modified by a request.
type lockTarget struct {
	path string // clean
	// members is set if the request also modifies or removes all the members
	// of the resource
	members bool
	// parent is set if the resource is the collection of a member that the
	// request creates, removes or renames
	parent bool
	// collection is set if the resource is known to be a collection
	collection bool
}

func resourceTarget(p string) lockTarget {
	return lockTarget{path: path.Clean(p), collection: strings.HasSuffix(p, "/")}
}

func treeTarget(p string) lockTarget {
	return lockTarget{path: path.Clean(p), members: true, collection: strings.HasSuffix(p, "/")}
}

func parentTarget(p string) lockTarget {
	return lockTarget{path: path.Dir(path.Clean(p)), parent: true}
}

// missingLocks returns the roots of the locks protecting the targets whose
// tokens the request didn't submit.
//
// A resource locked by several shared locks can be modified with the token
// of any of them, so their roots are only returned if none was submitted.
func (b *backend) missingLocks(r *http.Request, targets ...lockTarget) ([]string, error) {
	var missing []string
	seen := make(map[string]bool)
	for _, t := range targets {
		locks, err := b.locks.Locks(r.Context(), t.path, t.members)
		if err != nil {
			return nil, err
		}

		// The resources whose locks are needed: the target, and the
		// locked members of the target
		paths := []string{t.path}
		if t.members {
			for _, lock := range locks {
				if isPathBelow(lock.Root, t.path) {
					paths = append(paths, lock.Root)
				}
			}
		}

		for _, p := range paths {
			var needed []*Lock
			submitted := false
			for i := range locks {
				if locks[i].inScope(p) {
					needed = append(needed, &locks[i])
					submitted = submitted || b.submitted[locks[i].Token]
				}
			}
			if submitted {
				continue
			}
			for _, lock := range needed {
				if seen[lock.Token] {
					continue
				}
				seen[lock.Token] = true
				href := lock.Root
				if t.parent || isPathBelow(t.path, lock.Root) || (t.collection && lock.Root == t.path) {
					href = collectionHref(lock.Root)
				}
				missing = append(missing, href)
			}
		}
	}
	return missing, nil
}

// checkLocks fails with 423 Locked and the DAV:lock-token-submitted
// precondition unless the request submits the tokens of the locks protecting
// the targets (RFC 4918 sections 7.4 and 7.5): the locks whose scope includes
// them, and, for targets whose members are modified, the locks of their
// members.
func (b *backend) checkLocks(r *http.Request, targets ...lockTarget) error {
	missing, err := b.missingLocks(r, targets...)
	if err != nil {
		return err
	} else if len(missing) > 0 {
		return newPreconditionError(http.StatusLocked, "lock-token-submitted", missing...)
	}
	return nil
}

// checkWriteLocks checks the locks protecting a request writing the resource
// at p, which creates it if it doesn't exist: the locks of the resource, and,
// if the request creates it, the locks of its parent collection.
func (b *backend) checkWriteLocks(r *http.Request, p string) error {
	if err := b.checkLocks(r, resourceTarget(p)); err != nil {
		return err
	}
	missing, err := b.missingLocks(r, parentTarget(p))
	if err != nil || len(missing) == 0 {
		return err
	}
	// Modifying an existing member of a locked collection doesn't need the
	// collection's lock
	if _, err := b.FileSystem.Stat(r.Context(), p); err == nil {
		return nil
	} else if !internal.IsNotFound(err) {
		return err
	}
	return newPreconditionError(http.StatusLocked, "lock-token-submitted", missing...)
}

// treeLocks returns the locks rooted at p or below, which a request deleting,
// moving or overwriting the resources there removes.
func (b *backend) treeLocks(r *http.Request, p string) ([]Lock, error) {
	p = path.Clean(p)
	locks, err := b.locks.Locks(r.Context(), p, true)
	if err != nil {
		return nil, err
	}
	var l []Lock
	for _, lock := range locks {
		if lock.Root == p || isPathBelow(lock.Root, p) {
			l = append(l, lock)
		}
	}
	return l, nil
}

// removeLocks removes locks listed by treeLocks once a request has deleted,
// moved away or overwritten their resources (RFC 4918 sections 9.6, 9.8.4
// and 9.9.3), and returned opErr. Locks created meanwhile, once the resources
// were gone, are kept. So are the locks rooted at keep, the destination of a
// COPY or MOVE request: the new resource is added to them (section 7.6). If
// opErr reports a partial failure (see internal.HrefErrorResponses), only the
// locks of the resources which are gone are removed.
//
// The request has been carried out, so errors are ignored: a lock left
// behind on an unmapped URL is dropped once it expires or is unlocked.
func (b *backend) removeLocks(r *http.Request, locks []Lock, keep string, opErr error) {
	if opErr != nil {
		if _, err := internal.HrefErrorResponses(opErr); err != nil {
			// The request failed as a whole
			return
		}
	}
	for _, lock := range locks {
		if lock.Root == keep {
			continue
		}
		if opErr != nil {
			if _, err := b.FileSystem.Stat(r.Context(), lock.Root); !internal.IsNotFound(err) {
				continue
			}
		}
		b.locks.Unlock(r.Context(), lock.Token)
	}
}

// findLock returns the lock with the given token whose scope includes the
// Request-URI.
func (b *backend) findLock(r *http.Request, token string) (*Lock, error) {
	locks, err := b.locks.Locks(r.Context(), path.Clean(r.URL.Path), false)
	if err != nil {
		return nil, err
	}
	for _, lock := range locks {
		if lock.Token == token {
			return &lock, nil
		}
	}
	return nil, nil
}

// lockTimeout returns the time left before a lock expires, zero meaning
// never.
func lockTimeout(lock *Lock) time.Duration {
	if lock.Expires.IsZero() {
		return 0
	}
	d := time.Until(lock.Expires)
	if d <= 0 {
		// About to expire, but not infinite
		d = 1
	}
	return d
}

// newActiveLock returns the DAV:activelock element describing a lock, in the
// lockdiscovery property of the resource at the clean path p, a collection if
// isDir. timeout is the time left before the lock expires.
func newActiveLock(lock *Lock, timeout time.Duration, p string, isDir bool) internal.ActiveLock {
	al := internal.ActiveLock{
		LockType:  internal.LockType{Write: &struct{}{}},
		Depth:     internal.DepthZero,
		Timeout:   &internal.Timeout{Duration: timeout},
		LockToken: &internal.LockToken{Href: lock.Token},
	}
	if lock.Shared {
		al.LockScope.Shared = &struct{}{}
	} else {
		al.LockScope.Exclusive = &struct{}{}
	}
	if lock.Recursive {
		al.Depth = internal.DepthInfinity
	}
	if lock.Owner != "" {
		// A LockSystem may hold an invalid owner: leave it out
		if owner, err := internal.UnmarshalRawXML(lock.Owner); err == nil {
			al.Owner = owner
		}
	}
	root := lock.Root
	if isPathBelow(p, root) || (root == p && isDir) {
		root = collectionHref(root)
	}
	al.LockRoot.Href = internal.Href{Path: root}
	return al
}

// lockDiscovery returns the lockdiscovery property (RFC 4918 section 15.8)
// of the resource at the clean path p, a collection if isDir, with first a
// lock just created or refreshed, if any.
func (b *backend) lockDiscovery(r *http.Request, p string, isDir bool, first *Lock) (*internal.LockDiscovery, error) {
	locks, err := b.locks.Locks(r.Context(), p, false)
	if err != nil {
		return nil, err
	}
	discovery := &internal.LockDiscovery{}
	if first != nil {
		discovery.ActiveLock = append(discovery.ActiveLock, newActiveLock(first, first.Timeout, p, isDir))
	}
	for i := range locks {
		if first == nil || locks[i].Token != first.Token {
			discovery.ActiveLock = append(discovery.ActiveLock, newActiveLock(&locks[i], lockTimeout(&locks[i]), p, isDir))
		}
	}
	return discovery, nil
}

// propFindLocks lists the locks of the resources of a PROPFIND response,
// fetching them once, when the lockdiscovery property is first needed.
type propFindLocks struct {
	b       *backend
	r       *http.Request
	path    string // clean path of the Request-URI
	members bool   // the response includes the members of the resource

	fetched bool
	locks   []Lock
	err     error
}

func (pl *propFindLocks) discovery(fi *FileInfo) (*internal.LockDiscovery, error) {
	if !pl.fetched {
		pl.locks, pl.err = pl.b.locks.Locks(pl.r.Context(), pl.path, pl.members)
		pl.fetched = true
	}
	if pl.err != nil {
		return nil, pl.err
	}
	p := path.Clean(fi.Path)
	discovery := &internal.LockDiscovery{}
	for i := range pl.locks {
		if pl.locks[i].inScope(p) {
			discovery.ActiveLock = append(discovery.ActiveLock, newActiveLock(&pl.locks[i], lockTimeout(&pl.locks[i]), p, fi.IsDir))
		}
	}
	return discovery, nil
}

// lockConflictError returns the error of a LOCK request whose lock conflicts
// with existing locks (RFC 4918 section 9.10.6). If some of these locks are
// rooted at members of the Request-URI, it lists them, failing the
// Request-URI with 424 Failed Dependency, to be served in a multi-status
// response (section 9.10.9).
func (b *backend) lockConflictError(r *http.Request, lock *Lock) error {
	locks, err := b.locks.Locks(r.Context(), lock.Root, lock.Recursive)
	if err != nil {
		return err
	}
	var hrefs, members []string
	for i := range locks {
		if !lock.conflicts(&locks[i]) {
			continue
		}
		switch root := locks[i].Root; {
		case isPathBelow(lock.Root, root):
			hrefs = append(hrefs, collectionHref(root))
		case root == lock.Root:
			hrefs = append(hrefs, r.URL.Path)
		default:
			members = append(members, root)
		}
	}
	if len(members) > 0 && len(hrefs) == 0 {
		errs := make([]error, 0, len(members)+1)
		for _, member := range members {
			errs = append(errs, NewHrefError(member, newPreconditionError(http.StatusLocked, "no-conflicting-lock")))
		}
		errs = append(errs, NewHrefError(r.URL.Path, &internal.HTTPError{Code: http.StatusFailedDependency}))
		return errors.Join(errs...)
	}
	return newPreconditionError(http.StatusLocked, "no-conflicting-lock", append(hrefs, members...)...)
}

// Lock creates or refreshes a lock (RFC 4918 section 9.10).
func (b lockingBackend) Lock(r *http.Request, req *internal.LockRequest) (string, *internal.LockDiscovery, bool, error) {
	p := path.Clean(r.URL.Path)
	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if internal.IsNotFound(err) {
		fi = nil
	} else if err != nil {
		return "", nil, false, err
	}
	isDir := fi != nil && fi.IsDir

	if req.Info == nil {
		// RFC 4918 section 9.10.2
		found, err := b.findLock(r, req.RefreshToken)
		if err != nil {
			return "", nil, false, err
		} else if found == nil {
			return "", nil, false, newPreconditionError(http.StatusPreconditionFailed, "lock-token-matches-request-uri")
		}
		// A refresh restarts the timeout of the lock (section 6.6), and may
		// change it
		timeout := found.Timeout
		if req.Timeout != nil {
			timeout = req.Timeout.Duration
		}
		lock, err := b.locks.Refresh(r.Context(), req.RefreshToken, timeout)
		if errors.Is(err, ErrNoSuchLock) {
			return "", nil, false, newPreconditionError(http.StatusPreconditionFailed, "lock-token-matches-request-uri")
		} else if err != nil {
			return "", nil, false, err
		}
		discovery, err := b.lockDiscovery(r, p, isDir, &lock)
		return lock.Token, discovery, false, err
	}

	// Locking an unmapped URL creates a non-collection resource in the
	// parent collection (RFC 4918 section 7.3)
	if fi == nil {
		if strings.HasSuffix(r.URL.Path, "/") {
			return "", nil, false, internal.HTTPErrorf(http.StatusConflict, "webdav: locking an unmapped URL creates a non-collection resource, which can't end with a slash")
		}
		if err := b.checkLocks(r, parentTarget(p)); err != nil {
			return "", nil, false, err
		}
	}

	lock := Lock{
		Root: p,
		// "Depth: infinity" only makes a difference for collections, but is
		// reported as requested
		Recursive: req.Depth == internal.DepthInfinity,
		Shared:    req.Info.LockScope.Shared != nil,
	}
	if req.Timeout != nil {
		lock.Timeout = req.Timeout.Duration
	}
	if req.Info.Owner != nil {
		lock.Owner, err = internal.MarshalRawXML(req.Info.Owner)
		if err != nil {
			return "", nil, false, &internal.HTTPError{Code: http.StatusBadRequest, Err: err}
		}
	}
	granted, err := b.locks.Create(r.Context(), lock)
	if errors.Is(err, ErrLocked) {
		return "", nil, false, b.lockConflictError(r, &lock)
	} else if err != nil {
		return "", nil, false, err
	}
	lock = granted

	created := false
	if fi == nil {
		// The resource is created once the lock is held, so that no one
		// else can write it in between. It's empty: the lock owner will
		// PUT its contents.
		opts := CreateOptions{IfNoneMatch: "*"}
		_, _, err := b.FileSystem.Create(r.Context(), r.URL.Path, io.NopCloser(strings.NewReader("")), &opts)
		if err != nil {
			b.locks.Unlock(r.Context(), lock.Token)
			var httpErr *internal.HTTPError
			if internal.IsNotFound(err) {
				// The parent collection doesn't exist
				return "", nil, false, &internal.HTTPError{Code: http.StatusConflict, Err: err}
			} else if errors.As(err, &httpErr) && httpErr.Code == http.StatusPreconditionFailed {
				// Created concurrently
				return "", nil, false, &internal.HTTPError{Code: http.StatusConflict, Err: err}
			}
			return "", nil, false, err
		}
		created = true
	}

	discovery, err := b.lockDiscovery(r, p, isDir, &lock)
	return lock.Token, discovery, created, err
}

// Unlock removes a lock whose scope includes the Request-URI (RFC 4918
// section 9.11).
func (b lockingBackend) Unlock(r *http.Request, token string) error {
	lock, err := b.findLock(r, token)
	if err != nil {
		return err
	} else if lock == nil {
		return newPreconditionError(http.StatusConflict, "lock-token-matches-request-uri")
	}
	err = b.locks.Unlock(r.Context(), token)
	if errors.Is(err, ErrNoSuchLock) {
		return newPreconditionError(http.StatusConflict, "lock-token-matches-request-uri")
	}
	return err
}
