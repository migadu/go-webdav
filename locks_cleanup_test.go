package webdav

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// RFC 4918 section 7.6: a resource moved or copied onto a locked destination
// is added to the lock of the destination. Editors saving by renaming a
// temporary file over the original keep their lock.
func TestOverwriteLockedDestination(t *testing.T) {
	for _, method := range []string{"MOVE", "COPY"} {
		t.Run(method, func(t *testing.T) {
			lt := newLockTest(t)
			token := lt.lockFile()
			lt.expect(http.StatusCreated, http.MethodPut, "/tmp.txt", "new")
			lt.expect(http.StatusNoContent, method, "/tmp.txt", "", "Destination", "/file.txt", "If", "</file.txt> (<"+token+">)")
			if b, err := os.ReadFile(filepath.Join(lt.dir, "file.txt")); err != nil || string(b) != "new" {
				t.Fatalf("file.txt = %q, %v, want new", b, err)
			}

			w := lt.expect(http.StatusMultiStatus, "PROPFIND", "/file.txt", propFindLockDiscovery, "Depth", "0")
			if locks := lockDiscovery(t, w)["/file.txt"]; len(locks) != 1 || locks[0].Token != token {
				t.Errorf("lockdiscovery after %v = %+v, want the lock", method, locks)
			}
			lt.expect(http.StatusLocked, http.MethodPut, "/file.txt", "")
			lt.expect(http.StatusNoContent, http.MethodPut, "/file.txt", "", "If", "(<"+token+">)")
			lt.expect(http.StatusNoContent, "UNLOCK", "/file.txt", "", "Lock-Token", "<"+token+">")
		})
	}
}

// The locks of the members of an overwritten collection are gone with them.
func TestOverwriteDestinationMemberLocks(t *testing.T) {
	lt := newLockTest(t)
	if err := os.MkdirAll(filepath.Join(lt.dir, "dir2"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lt.dir, "dir2", "b.txt"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	coll := lt.createLock(Lock{Root: "/dir2"})
	member := lt.createLock(Lock{Root: "/dir2/b.txt"})
	lt.expect(http.StatusNoContent, "COPY", "/dir/", "", "Destination", "/dir2/", "If", "</dir2/> (<"+coll+">) </dir2/b.txt> (<"+member+">)")
	locks, _ := lt.ls.Locks(context.Background(), "/", true)
	if got := lockRoots(locks); got != "/dir2" {
		t.Errorf("locks after COPY = [%v], want [/dir2]", got)
	}
}

// lockingFileSystem runs a function once a resource has been removed, as if
// another request had come in meanwhile.
type lockingFileSystem struct {
	LocalFileSystem
	afterRemove func()
}

func (fs lockingFileSystem) RemoveAll(ctx context.Context, name string, opts *RemoveAllOptions) error {
	err := fs.LocalFileSystem.RemoveAll(ctx, name, opts)
	fs.afterRemove()
	return err
}

// A DELETE only removes the locks the resources had when it was checked, not
// a lock created by another client once the resource is gone.
func TestDeleteKeepsNewerLocks(t *testing.T) {
	lt := newLockTest(t)
	var other string
	lt.h.FileSystem = lockingFileSystem{
		LocalFileSystem: LocalFileSystem(lt.dir),
		afterRemove: func() {
			other = lt.createLock(Lock{Root: "/file.txt"})
		},
	}
	lt.expect(http.StatusNoContent, http.MethodDelete, "/file.txt", "")
	locks, _ := lt.ls.Locks(context.Background(), "/file.txt", false)
	if len(locks) != 1 || locks[0].Token != other {
		t.Errorf("locks after DELETE = %+v, want the lock created meanwhile", locks)
	}
}

// failingLockSystem is a LockSystem whose Unlock fails.
type failingLockSystem struct {
	*MemLockSystem
}

func (failingLockSystem) Unlock(ctx context.Context, token string) error {
	return errors.New("lock store unavailable")
}

// Once a request has succeeded, it isn't reported as failed because its
// locks couldn't be removed.
func TestRemoveLocksError(t *testing.T) {
	lt := newLockTest(t)
	ls := failingLockSystem{lt.ls}
	lt.h.LockSystem = ls
	token := lt.lockFile()
	lt.expect(http.StatusNoContent, http.MethodDelete, "/file.txt", "", "If", "(<"+token+">)")
	if _, err := os.Stat(filepath.Join(lt.dir, "file.txt")); !os.IsNotExist(err) {
		t.Errorf("file.txt still exists: %v", err)
	}
}
