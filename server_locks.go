package webdav

import (
	"net/http"
	"path"
	"time"

	"github.com/emersion/go-webdav/internal"
)

// lockingBackend is the backend of a Handler with a LockSystem: it supports
// LOCK and UNLOCK requests.
type lockingBackend struct {
	*backend
}

var _ internal.LockBackend = lockingBackend{}

// resourceLock returns the lock rooted at the resource at p, if any.
func (b *backend) resourceLock(r *http.Request, p string) (*Lock, error) {
	p = path.Clean(p)
	locks, err := b.locks.Locks(r.Context(), p, false)
	if err != nil {
		return nil, err
	}
	for _, lock := range locks {
		if lock.Root == p {
			return &lock, nil
		}
	}
	return nil, nil
}

// checkLock fails unless the resource at p isn't locked, or the request
// submits the lock token in the If header field.
func (b *backend) checkLock(r *http.Request, p string) error {
	lock, err := b.resourceLock(r, p)
	if err != nil || lock == nil {
		return err
	}
	token, err := internal.ParseSubmittedToken(r.Header)
	if err != nil {
		return err
	}
	if token != lock.Token {
		return &internal.HTTPError{Code: http.StatusLocked}
	}
	return nil
}

// checkMoveLocks fails unless the source and destination of a MOVE request
// aren't locked, or the request submits their lock tokens in the If header
// field.
func (b *backend) checkMoveLocks(r *http.Request, dest *internal.Href) error {
	var conditions [][]internal.Condition
	if hif := r.Header.Get("If"); hif != "" {
		var err error
		conditions, err = internal.ParseConditions(hif)
		if err != nil {
			return &internal.HTTPError{http.StatusBadRequest, err}
		}
	}
	srcLock, err := b.resourceLock(r, r.URL.Path)
	if err != nil {
		return err
	}
	destLock, err := b.resourceLock(r, dest.Path)
	if err != nil {
		return err
	}
	for _, conds := range conditions {
		if len(conds) == 0 {
			continue
		}
		if len(conds) > 1 {
			return internal.HTTPErrorf(http.StatusBadRequest, "webdav: multiple conditions are not supported in the If header field")
		}
		if (conds[0].Resource == "" || conds[0].Resource == r.URL.Path) && srcLock != nil && conds[0].Token == srcLock.Token {
			srcLock = nil
		} else if (conds[0].Resource == dest.Path) && destLock != nil && conds[0].Token == destLock.Token {
			destLock = nil
		}
	}
	if srcLock != nil || destLock != nil {
		return &internal.HTTPError{Code: http.StatusLocked}
	}
	return nil
}

// removeLocks removes the locks rooted at p or below, once the resources
// there are gone.
func (b *backend) removeLocks(r *http.Request, p string) error {
	return b.locks.RemoveAll(r.Context(), path.Clean(p))
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
		lock, err := b.resourceLock(r, r.URL.Path)
		if err != nil {
			return nil, false, err
		} else if lock == nil || lock.Token != refreshToken {
			return nil, false, &internal.HTTPError{Code: http.StatusPreconditionFailed}
		}
		// Lock timeout is not supported so refresh is a no-op
		return &internal.Lock{Href: lock.Token, Root: lock.Root}, false, nil
	}

	lock, err := b.locks.Create(r.Context(), Lock{Root: path.Clean(r.URL.Path)})
	if err != nil {
		return nil, false, internal.HTTPErrorf(http.StatusLocked, "webdav: there is already a lock on this resource")
	}
	return &internal.Lock{Href: lock.Token, Root: fi.Path}, false, nil
}

func (b lockingBackend) Unlock(r *http.Request, tokenHref string) error {
	if lock, err := b.resourceLock(r, r.URL.Path); err != nil {
		return err
	} else if lock == nil {
		return internal.HTTPErrorf(http.StatusConflict, "webdav: resource is not locked")
	} else if lock.Token != tokenHref {
		return internal.HTTPErrorf(http.StatusForbidden, "webdav: incorrect token")
	}
	return b.locks.Unlock(r.Context(), tokenHref)
}
