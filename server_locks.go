package webdav

import (
	"encoding/xml"
	"errors"
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
	values := r.Header.Values("If")
	if len(values) == 0 {
		return nil
	}
	lists, err := internal.ParseConditions(strings.Join(values, " "))
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
func (b *backend) missingLocks(r *http.Request, targets ...lockTarget) ([]string, error) {
	var missing []string
	seen := make(map[string]bool)
	for _, t := range targets {
		locks, err := b.locks.Locks(r.Context(), t.path, t.members)
		if err != nil {
			return nil, err
		}
		for _, lock := range locks {
			if b.submitted[lock.Token] || seen[lock.Token] {
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

// removeLocks removes the locks rooted at p or below once a request has
// deleted, moved away or overwritten the resources there (RFC 4918 sections
// 9.6, 9.8.4 and 9.9.3), and returned opErr. If opErr reports a partial
// failure (see internal.HrefErrorResponses), only the locks of the resources
// which are gone are removed.
func (b *backend) removeLocks(r *http.Request, p string, opErr error) error {
	p = path.Clean(p)
	if opErr == nil {
		return b.locks.RemoveAll(r.Context(), p)
	}
	if _, err := internal.HrefErrorResponses(opErr); err != nil {
		// The request failed as a whole
		return opErr
	}

	// The request already failed partially, so its response is the list of
	// failures: lock errors are ignored
	locks, err := b.locks.Locks(r.Context(), p, true)
	if err != nil {
		return opErr
	}
	for _, lock := range locks {
		if lock.Root != p && !isPathBelow(lock.Root, p) {
			continue
		}
		if _, err := b.FileSystem.Stat(r.Context(), lock.Root); internal.IsNotFound(err) {
			b.locks.Unlock(r.Context(), lock.Token)
		}
	}
	return opErr
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

func (b lockingBackend) Lock(r *http.Request, depth internal.Depth, timeout time.Duration, refreshToken string) (*internal.Lock, bool, error) {
	// TODO: locking unmapped URLs
	fi, err := b.FileSystem.Stat(r.Context(), r.URL.Path)
	if err != nil {
		return nil, false, err
	}
	if fi.IsDir {
		return nil, false, internal.HTTPErrorf(http.StatusBadRequest, "webdav: locking collections is not supported")
	}

	if refreshToken != "" {
		// RFC 4918 section 9.10.2
		lock, err := b.findLock(r, refreshToken)
		if err != nil {
			return nil, false, err
		} else if lock == nil {
			return nil, false, newPreconditionError(http.StatusPreconditionFailed, "lock-token-matches-request-uri")
		}
		refreshed, err := b.locks.Refresh(r.Context(), refreshToken, lock.Timeout)
		if errors.Is(err, ErrNoSuchLock) {
			return nil, false, newPreconditionError(http.StatusPreconditionFailed, "lock-token-matches-request-uri")
		} else if err != nil {
			return nil, false, err
		}
		return &internal.Lock{Href: refreshed.Token, Root: fi.Path, Timeout: refreshed.Timeout}, false, nil
	}

	lock, err := b.locks.Create(r.Context(), Lock{Root: path.Clean(r.URL.Path)})
	if errors.Is(err, ErrLocked) {
		return nil, false, newPreconditionError(http.StatusLocked, "no-conflicting-lock")
	} else if err != nil {
		return nil, false, err
	}
	return &internal.Lock{Href: lock.Token, Root: fi.Path}, false, nil
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
