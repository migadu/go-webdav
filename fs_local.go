package webdav

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/emersion/go-webdav/internal"
)

// LocalFileSystem implements FileSystem for a local directory.
//
// Accesses are confined to the directory with os.Root: symbolic links are
// only followed if they are relative and resolve to a location inside the
// directory. Copying a collection copies the symbolic links it contains as
// links.
//
// Create writes an upload to a temporary file in the destination's
// directory, renamed to the destination once complete, so that a failed
// upload leaves an existing file alone. Overwriting a file thus needs write
// access to its directory, not only to the file: the file is replaced with a
// new one, which gets the permissions of the old one, but not its owner, and
// other hard links to the old file keep its old contents.
type LocalFileSystem string

var _ FileSystem = LocalFileSystem("")

// relPath converts a request path into a slash-separated path relative to the
// root directory, "." being the root directory itself.
func (fs LocalFileSystem) relPath(name string) (string, error) {
	if (filepath.Separator != '/' && strings.IndexRune(name, filepath.Separator) >= 0) || strings.Contains(name, "\x00") {
		return "", internal.HTTPErrorf(http.StatusBadRequest, "webdav: invalid character in path")
	}
	name = path.Clean(name)
	if !path.IsAbs(name) {
		return "", internal.HTTPErrorf(http.StatusBadRequest, "webdav: expected absolute path, got %q", name)
	}
	if name == "/" {
		return ".", nil
	}
	return name[1:], nil
}

// stagingPrefix starts the names of the temporary files Create writes uploads
// to, before renaming them to their destination. Those names aren't part of
// the file system served: files with such names aren't listed or copied,
// can't be accessed, and can't be created by clients.
const stagingPrefix = ".webdav-tmp-"

// isStagingPath reports whether p, a slash-separated path relative to the
// root directory, names a file with a staging name, or a file beneath one.
func isStagingPath(p string) bool {
	for _, name := range strings.Split(p, "/") {
		if strings.HasPrefix(name, stagingPrefix) {
			return true
		}
	}
	return false
}

// checkStagingPath returns an error with code if p is a staging path (see
// isStagingPath): 404 Not Found for a file which is to exist, 403 Forbidden
// for one which is to be created.
func checkStagingPath(p string, code int) error {
	if isStagingPath(p) {
		return NewHTTPError(code, fmt.Errorf("webdav: names starting with %q are reserved", stagingPrefix))
	}
	return nil
}

// openRoot opens the root directory. The caller must close it.
func (fs LocalFileSystem) openRoot() (*os.Root, error) {
	root, err := os.OpenRoot(string(fs))
	if err != nil {
		return nil, errFromOS(err)
	}
	return root, nil
}

func (fs LocalFileSystem) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	p, err := fs.relPath(name)
	if err != nil {
		return nil, err
	}
	if err := checkStagingPath(p, http.StatusNotFound); err != nil {
		return nil, err
	}
	root, err := fs.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()

	f, err := root.Open(p)
	if err != nil {
		return nil, errFromOS(err)
	}
	return f, nil
}

func fileInfoFromOS(p string, fi os.FileInfo) *FileInfo {
	return &FileInfo{
		Path:    p,
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
		IsDir:   fi.IsDir(),
		// TODO: fallback to http.DetectContentType?
		MIMEType: mime.TypeByExtension(path.Ext(p)),
		// RFC 2616 section 13.3.3 describes strong ETags. Ideally these would
		// be checksums or sequence numbers, however these are expensive to
		// compute. The modification time with nanosecond granularity is good
		// enough, as it's very unlikely for the same file to be modified twice
		// during a single nanosecond.
		ETag: fmt.Sprintf("%x%x", fi.ModTime().UnixNano(), fi.Size()),
	}
}

func errFromOS(err error) error {
	// Remove path from path errors so it's not returned to the user
	var perr *iofs.PathError
	if errors.As(err, &perr) {
		err = fmt.Errorf("%s: %w", perr.Op, perr.Err)
	}

	if isNotExist(err) {
		return NewHTTPError(http.StatusNotFound, err)
	} else if errors.Is(err, iofs.ErrPermission) || isPathEscapesError(err) {
		return NewHTTPError(http.StatusForbidden, err)
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		return NewHTTPError(http.StatusServiceUnavailable, err)
	} else {
		return err
	}
}

// isNotExist reports whether err means that a path names no file: either it
// doesn't exist, or a component of its parent path, or of the target of a
// symbolic link it resolves through, isn't a directory but a file (ENOTDIR),
// which iofs.ErrNotExist doesn't match.
func isNotExist(err error) bool {
	return errors.Is(err, iofs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// isPathEscapesError reports whether an os.Root method failed because the path
// refers to a location outside of the root, e.g. via a symbolic link. The os
// package doesn't export this error, so match its message.
func isPathEscapesError(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err.Error() == "path escapes from parent" {
			return true
		}
	}
	return false
}

func statRoot(root *os.Root, p, name string) (*FileInfo, error) {
	fi, err := root.Stat(p)
	if err != nil {
		return nil, errFromOS(err)
	}
	return fileInfoFromOS(name, fi), nil
}

func (fs LocalFileSystem) Stat(ctx context.Context, name string) (*FileInfo, error) {
	p, err := fs.relPath(name)
	if err != nil {
		return nil, err
	}
	if err := checkStagingPath(p, http.StatusNotFound); err != nil {
		return nil, err
	}
	root, err := fs.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()

	return statRoot(root, p, name)
}

// walkDirInfo returns information about the file p visited by fs.WalkDir,
// called with root.FS() and the starting directory dir.
func walkDirInfo(root *os.Root, dir, p string, d iofs.DirEntry) (os.FileInfo, error) {
	if p == dir {
		// fs.WalkDir has already called root.Stat for the starting directory
		return d.Info()
	}
	// Before Go 1.26, DirEntry.Info isn't confined to the root
	return root.Lstat(p)
}

func (fs LocalFileSystem) ReadDir(ctx context.Context, name string, recursive bool) ([]FileInfo, error) {
	dir, err := fs.relPath(name)
	if err != nil {
		return nil, err
	}
	if err := checkStagingPath(dir, http.StatusNotFound); err != nil {
		return nil, err
	}
	root, err := fs.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var l []FileInfo
	err = iofs.WalkDir(root.FS(), dir, func(p string, d iofs.DirEntry, err error) error {
		// Skipped before it's stat'ed: once the upload is done, it's gone
		if p != dir && strings.HasPrefix(d.Name(), stagingPrefix) {
			if d.IsDir() {
				return iofs.SkipDir
			}
			return nil
		}

		var fi os.FileInfo
		if err == nil {
			fi, err = walkDirInfo(root, dir, p, d)
		}
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				return nil
			}
			return err
		}

		entryPath := "/" + p
		if p == "." {
			entryPath = "/"
		}
		l = append(l, *fileInfoFromOS(entryPath, fi))

		if !recursive && fi.IsDir() && p != dir {
			return iofs.SkipDir
		}
		return nil
	})
	return l, errFromOS(err)
}

func checkConditionalMatches(fi *FileInfo, ifMatch, ifNoneMatch ConditionalMatch) error {
	etag := ""
	if fi != nil {
		etag = fi.ETag
	}

	if ifMatch.IsSet() {
		if ok, err := ifMatch.MatchETag(etag); err != nil {
			return NewHTTPError(http.StatusBadRequest, err)
		} else if !ok {
			return NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("If-Match condition failed"))
		}
	}

	if ifNoneMatch.IsSet() {
		if ok, err := ifNoneMatch.MatchETagWeak(etag); err != nil {
			return NewHTTPError(http.StatusBadRequest, err)
		} else if ok {
			return NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("If-None-Match condition failed"))
		}
	}

	return nil
}

// checkParentDir checks that the parent collection of p exists and is a
// directory, returning 409 Conflict when it does not (RFC 4918 sections 9.3.1,
// 9.7.1, 9.8.5 and 9.9.4).
func checkParentDir(root *os.Root, p string) error {
	parent := path.Dir(p)
	if parent == "." {
		return nil
	}
	fi, err := root.Stat(parent)
	if err != nil {
		if isNotExist(err) {
			return NewHTTPError(http.StatusConflict, err)
		}
		return errFromOS(err)
	}
	if !fi.IsDir() {
		return NewHTTPError(http.StatusConflict, fmt.Errorf("webdav: parent is not a collection"))
	}
	return nil
}

func createTempInRoot(root *os.Root, dir string, perm os.FileMode) (*os.File, string, error) {
	for i := 0; i < 16; i++ {
		tmpName := stagingPrefix + rand.Text()
		tmpPath := tmpName
		if dir != "." {
			tmpPath = path.Join(dir, tmpName)
		}
		f, err := root.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, iofs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", errFromOS(err)
		}
		return f, tmpPath, nil
	}
	return nil, "", fmt.Errorf("webdav: failed to allocate temporary file")
}

func (fs LocalFileSystem) Create(ctx context.Context, name string, body io.ReadCloser, opts *CreateOptions) (fi *FileInfo, created bool, err error) {
	p, err := fs.relPath(name)
	if err != nil {
		return nil, false, err
	}
	if p == "." {
		return nil, false, NewHTTPError(http.StatusMethodNotAllowed, fmt.Errorf("webdav: cannot overwrite root collection with a file"))
	}
	if err := checkStagingPath(p, http.StatusForbidden); err != nil {
		return nil, false, err
	}
	root, err := fs.openRoot()
	if err != nil {
		return nil, false, err
	}
	defer root.Close()

	if err := checkParentDir(root, p); err != nil {
		return nil, false, err
	}

	origStat, err := root.Stat(p)
	if err != nil {
		// A symbolic link which doesn't resolve is replaced
		if !isNotExist(err) {
			return nil, false, errFromOS(err)
		}
	} else {
		if origStat.IsDir() {
			return nil, false, NewHTTPError(http.StatusMethodNotAllowed, fmt.Errorf("webdav: cannot overwrite collection with a file"))
		}
		fi = fileInfoFromOS(name, origStat)
	}
	created = fi == nil

	if err := checkConditionalMatches(fi, opts.IfMatch, opts.IfNoneMatch); err != nil {
		return nil, false, err
	}

	perm := os.FileMode(0666)
	if origStat != nil {
		perm = origStat.Mode() & os.ModePerm
	}
	wc, tmpPath, err := createTempInRoot(root, path.Dir(p), perm)
	if err != nil {
		return nil, false, err
	}
	defer wc.Close()
	if origStat != nil {
		// Unlike the mode a file is created with, Chmod isn't masked by
		// the umask: the new file gets all of the old one's permissions.
		// Some file systems don't support it, but their files have no
		// permissions of their own to keep.
		wc.Chmod(perm)
	}

	if _, err := io.Copy(wc, body); err != nil {
		wc.Close()
		root.Remove(tmpPath)
		return nil, false, err
	}
	if err := wc.Close(); err != nil {
		root.Remove(tmpPath)
		return nil, false, err
	}
	if err := root.Rename(tmpPath, p); err != nil {
		root.Remove(tmpPath)
		return nil, false, errFromOS(err)
	}

	fi, err = statRoot(root, p, name)
	if err != nil {
		return nil, false, err
	}

	return fi, created, err
}

// removeAll removes p and any children it contains from root.
func removeAll(root *os.Root, p string) error {
	if p == "." {
		// os.Root can't remove the root directory itself
		return NewHTTPError(http.StatusForbidden, fmt.Errorf("webdav: cannot remove the root directory"))
	}
	return errFromOS(root.RemoveAll(p))
}

func (fs LocalFileSystem) RemoveAll(ctx context.Context, name string, opts *RemoveAllOptions) error {
	p, err := fs.relPath(name)
	if err != nil {
		return err
	}
	if err := checkStagingPath(p, http.StatusNotFound); err != nil {
		return err
	}
	root, err := fs.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()

	// WebDAV semantics are that it should return a "404 Not Found" error in
	// case the resource doesn't exist. We need to Stat before RemoveAll.
	fi, err := statRoot(root, p, name)
	if err != nil {
		return err
	}

	if err := checkConditionalMatches(fi, opts.IfMatch, opts.IfNoneMatch); err != nil {
		return err
	}

	return removeAll(root, p)
}

func (fs LocalFileSystem) Mkdir(ctx context.Context, name string) error {
	p, err := fs.relPath(name)
	if err != nil {
		return err
	}
	if err := checkStagingPath(p, http.StatusForbidden); err != nil {
		return err
	}
	root, err := fs.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()

	if err := checkParentDir(root, p); err != nil {
		return err
	}

	if err := root.Mkdir(p, 0755); os.IsExist(err) {
		return NewHTTPError(http.StatusMethodNotAllowed, err)
	} else {
		return errFromOS(err)
	}
}

func copyRegularFile(root *os.Root, src, dst string, perm os.FileMode) error {
	srcFile, err := root.Open(src)
	if err != nil {
		return errFromOS(err)
	}
	defer srcFile.Close()

	dstFile, err := root.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, perm)
	if isNotExist(err) {
		return NewHTTPError(http.StatusConflict, err)
	} else if err != nil {
		return errFromOS(err)
	}
	defer dstFile.Close()
	// As in Create, Chmod isn't masked by the umask, unlike the mode the
	// file is created with
	dstFile.Chmod(perm)

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return dstFile.Close()
}

// isLocalSymlink reports whether a symbolic link at p, a slash-separated path
// relative to the root directory, pointing to target resolves to a location
// inside the root.
func isLocalSymlink(p, target string) bool {
	// Reject absolute targets, and on Windows targets relative to a drive or
	// to the root of the current drive
	if filepath.IsAbs(target) || filepath.VolumeName(target) != "" || strings.HasPrefix(filepath.ToSlash(target), "/") {
		return false
	}
	return filepath.IsLocal(filepath.Join(filepath.FromSlash(path.Dir(p)), target))
}

// copySymlink copies the symbolic link src to dst. root.Symlink doesn't check
// the target of the link, so refuse links which would point outside of the
// root once copied.
func copySymlink(root *os.Root, src, dst string) error {
	target, err := root.Readlink(src)
	if err != nil {
		return errFromOS(err)
	}
	if !isLocalSymlink(dst, target) {
		return NewHTTPError(http.StatusForbidden, fmt.Errorf("webdav: cannot copy a symbolic link pointing outside of the root directory"))
	}
	return errFromOS(root.Symlink(target, dst))
}

// isSubPath reports whether p is located beneath dir. Both paths are cleaned
// and relative to the root directory.
func isSubPath(dir, p string) bool {
	if dir == "." {
		return p != "."
	}
	return strings.HasPrefix(p, dir+"/")
}

// isWithin reports whether dir, a path to a directory relative to the root
// directory, resolves to ancestor or to a directory beneath it. The
// directories dir is in are dir/.., dir/../.. and so on: os.Root resolves
// ".." after following the symbolic links before it, so unlike the prefixes
// of dir, these are the directories dir really is in.
func isWithin(root *os.Root, dir string, ancestor os.FileInfo) bool {
	rootInfo, err := root.Stat(".")
	if err != nil {
		return false
	}
	for {
		fi, err := root.Stat(dir)
		if err != nil {
			return false
		}
		if os.SameFile(fi, ancestor) {
			return true
		}
		if os.SameFile(fi, rootInfo) {
			return false
		}
		dir += "/.."
	}
}

// isBeneath reports whether p, a path relative to the root directory, is
// located beneath the collection dirPath, which resolves to dirInfo: whether
// it's a path beneath dirPath, or the directory p is in is dirPath's or one
// beneath it under another name, through a symbolic link or an alias on a
// case-insensitive file system.
func isBeneath(root *os.Root, dirPath string, dirInfo os.FileInfo, p string) bool {
	return isSubPath(dirPath, p) || isWithin(root, path.Dir(p), dirInfo)
}

// errOverwriteSourceParent is the error of a COPY or MOVE request overwriting
// a collection which contains the source. The destination is removed first
// (RFC 4918 sections 9.8.4 and 9.9.3), which would remove the source with it.
func errOverwriteSourceParent() error {
	return NewHTTPError(http.StatusForbidden, fmt.Errorf("webdav: cannot overwrite a collection containing the source"))
}

// maxLinks is the number of symbolic links containsSource follows from the
// source, as many as Linux follows to resolve a path.
const maxLinks = 40

// containsSource reports whether removing dstPath, an existing destination,
// would remove srcPath with it: whether dstPath is an ancestor of srcPath, or
// the same directory as one under another name, through a symbolic link to
// it or an alias on a case-insensitive file system. If srcPath is a symbolic
// link, it also reports whether removing dstPath would remove what srcPath
// resolves to. A destination which is a symbolic link to such a directory is
// a link of its own (see isSeparateLink), removed without the source.
func containsSource(root *os.Root, srcPath, dstPath string) bool {
	if isSubPath(dstPath, srcPath) {
		return true
	}
	dstInfo, err := root.Stat(dstPath)
	if err != nil || !dstInfo.IsDir() {
		return false
	}
	// Removing a directory along the source's path, even a symbolic link,
	// leaves the source unreachable through that path
	for dir := path.Dir(srcPath); dir != "."; dir = path.Dir(dir) {
		fi, err := root.Stat(dir)
		if err == nil && os.SameFile(fi, dstInfo) && !isSeparateLink(root, dir, dstPath) {
			return true
		}
	}
	if fi, err := root.Lstat(dstPath); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	// Removing a directory removes what it really contains: the source,
	// and if it's a symbolic link, each link it resolves through and the
	// file it resolves to, each in the directory its path is relative to
	p := srcPath
	for i := 0; ; i++ {
		dir := "."
		if j := strings.LastIndex(p, "/"); j >= 0 {
			dir = p[:j]
		}
		if isWithin(root, dir, dstInfo) {
			return true
		}
		if i == maxLinks {
			return false
		}
		fi, err := root.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return false
		}
		target, err := root.Readlink(p)
		if err != nil {
			return false
		}
		// Not path.Join: ".." in the target applies after following
		// the links in dir, as os.Root does
		p = target
		if dir != "." {
			p = dir + "/" + target
		}
	}
}

// isSameFile reports whether srcPath and dstPath resolve to the same file,
// following symbolic links, or name the same symbolic link, which may not
// resolve.
func isSameFile(root *os.Root, srcPath, dstPath string) bool {
	srcInfo, srcErr := root.Lstat(srcPath)
	dstInfo, dstErr := root.Lstat(dstPath)
	if srcErr == nil && dstErr == nil && os.SameFile(srcInfo, dstInfo) {
		return true
	}
	srcInfo, srcErr = root.Stat(srcPath)
	dstInfo, dstErr = root.Stat(dstPath)
	return srcErr == nil && dstErr == nil && os.SameFile(srcInfo, dstInfo)
}

// isSameDirEntry reports whether dstPath is the directory entry srcPath
// names, under another name: an alias on a case-insensitive file system, for
// instance. Renaming srcPath to dstPath then only changes the entry's name.
// It returns false if both paths have the same name, through a symbolic link
// to their directory: there's nothing to rename.
func isSameDirEntry(root *os.Root, srcPath, dstPath string) bool {
	srcInfo, err := root.Lstat(srcPath)
	if err != nil {
		return false
	}
	dstInfo, err := root.Lstat(dstPath)
	if err != nil || !os.SameFile(srcInfo, dstInfo) {
		return false
	}
	srcName, dstName := path.Base(srcPath), path.Base(dstPath)
	if srcName == dstName {
		return false
	}
	srcDir, dstDir := path.Dir(srcPath), path.Dir(dstPath)
	srcDirInfo, err := root.Stat(srcDir)
	if err != nil {
		return false
	}
	dstDirInfo, err := root.Stat(dstDir)
	if err != nil || !os.SameFile(srcDirInfo, dstDirInfo) {
		return false
	}
	// As in isSeparateLink, compare the names listed rather than count the
	// entries for the file, which may have other hard links: dstName is an
	// alias of srcName if only srcName is listed. If another name matching
	// dstName ignoring case is listed, dstName may be an alias of that one,
	// another hard link to the file, which renaming onto would leave alone.
	entries, err := iofs.ReadDir(root.FS(), srcDir)
	if err != nil {
		return false
	}
	listed := false
	for _, e := range entries {
		if e.Name() == srcName {
			listed = true
		} else if strings.EqualFold(e.Name(), dstName) {
			return false
		}
	}
	return listed
}

// isSeparateLink reports whether dstPath, which resolves to the same file as
// srcPath, is a directory entry of its own, which can be removed without
// removing srcPath: a symbolic link, or another hard link to a regular file.
// It returns false if it can't tell, such as if srcPath is a symbolic link,
// which may resolve through dstPath, or if both paths may name the same
// entry: aliases on a case-insensitive file system, or paths through a
// symbolic link to their directory.
func isSeparateLink(root *os.Root, srcPath, dstPath string) bool {
	srcInfo, err := root.Lstat(srcPath)
	if err != nil || srcInfo.Mode()&os.ModeSymlink != 0 {
		return false
	}
	dstInfo, err := root.Lstat(dstPath)
	if err != nil {
		return false
	}
	if dstInfo.Mode()&os.ModeSymlink != 0 {
		// Removing a symbolic link leaves what it points to alone
		return true
	}
	if !srcInfo.Mode().IsRegular() || !os.SameFile(srcInfo, dstInfo) {
		return false
	}

	srcDir, dstDir := path.Dir(srcPath), path.Dir(dstPath)
	srcDirInfo, err := root.Stat(srcDir)
	if err != nil {
		return false
	}
	dstDirInfo, err := root.Stat(dstDir)
	if err != nil {
		return false
	}
	if !os.SameFile(srcDirInfo, dstDirInfo) {
		// Each entry belongs to a single directory
		return true
	}
	// In the same directory, the names are two entries only if both are
	// listed: otherwise one is an alias of the other, the file system
	// ignoring case for instance. This doesn't count the entries for the
	// file, which may have other hard links.
	srcName, dstName := path.Base(srcPath), path.Base(dstPath)
	if srcName == dstName {
		return false
	}
	entries, err := iofs.ReadDir(root.FS(), srcDir)
	if err != nil {
		return false
	}
	found := 0
	for _, e := range entries {
		if e.Name() == srcName || e.Name() == dstName {
			found++
		}
	}
	return found == 2
}

func (fs LocalFileSystem) Copy(ctx context.Context, src, dst string, options *CopyOptions) (created bool, err error) {
	srcPath, err := fs.relPath(src)
	if err != nil {
		return false, err
	}
	dstPath, err := fs.relPath(dst)
	if err != nil {
		return false, err
	}
	if err := checkStagingPath(srcPath, http.StatusNotFound); err != nil {
		return false, err
	}
	if err := checkStagingPath(dstPath, http.StatusForbidden); err != nil {
		return false, err
	}
	root, err := fs.openRoot()
	if err != nil {
		return false, err
	}
	defer root.Close()

	srcInfo, err := root.Stat(srcPath)
	if err != nil {
		return false, errFromOS(err)
	}

	// "Note that an infinite-depth COPY of /A/ into /A/B/ could lead to
	// infinite recursion if not handled correctly" (RFC 4918 section 9.8.3),
	// and /A/B/ may be under another name
	if srcInfo.IsDir() && !options.NoRecursive && isBeneath(root, srcPath, srcInfo, dstPath) {
		return false, NewHTTPError(http.StatusForbidden, fmt.Errorf("webdav: cannot copy a collection into itself"))
	}

	if err := checkParentDir(root, dstPath); err != nil {
		return false, err
	}

	if dstInfo, err := root.Stat(dstPath); err != nil {
		// A symbolic link which doesn't resolve is replaced
		if !isNotExist(err) {
			return false, errFromOS(err)
		}
		if _, lerr := root.Lstat(dstPath); lerr == nil {
			if options.NoOverwrite {
				return false, NewHTTPError(http.StatusPreconditionFailed, os.ErrExist)
			}
			if err := removeAll(root, dstPath); err != nil {
				return false, err
			}
		} else {
			created = true
		}
	} else {
		// A destination which is another link to the source file is
		// removed like any other (RFC 4918 section 9.8.4), leaving the
		// source alone: overwriting it in place would truncate the source
		if os.SameFile(srcInfo, dstInfo) && !isSeparateLink(root, srcPath, dstPath) {
			return false, NewHTTPError(http.StatusForbidden, fmt.Errorf("webdav: cannot copy a resource onto itself"))
		}
		if options.NoOverwrite {
			return false, NewHTTPError(http.StatusPreconditionFailed, os.ErrExist)
		}
		if containsSource(root, srcPath, dstPath) {
			return false, errOverwriteSourceParent()
		}
		if err := removeAll(root, dstPath); err != nil {
			return false, err
		}
	}

	// Directories are created with full permissions for their owner, so that
	// their members can be copied into them. Those whose source lacks some of
	// these permissions get the source's once the walk is done.
	type dirPerm struct {
		path string
		perm os.FileMode
	}
	var dirPerms []dirPerm

	err = iofs.WalkDir(root.FS(), srcPath, func(p string, d iofs.DirEntry, err error) error {
		if p != srcPath && strings.HasPrefix(d.Name(), stagingPrefix) {
			if d.IsDir() {
				return iofs.SkipDir
			}
			return nil
		}
		if err != nil {
			return err
		}
		fi, err := walkDirInfo(root, srcPath, p, d)
		if err != nil {
			return err
		}

		target := dstPath
		if p != srcPath {
			target = path.Join(dstPath, strings.TrimPrefix(p, srcPath+"/"))
		}
		perm := fi.Mode() & os.ModePerm

		switch {
		case fi.IsDir():
			if err := root.Mkdir(target, perm|0700); err != nil {
				return errFromOS(err)
			}
			// As in Create, Chmod isn't masked by the umask, unlike
			// the mode the directory is created with
			root.Chmod(target, perm|0700)
			if perm&0700 != 0700 {
				dirPerms = append(dirPerms, dirPerm{target, perm})
			}
		case fi.Mode()&os.ModeSymlink != 0:
			if err := copySymlink(root, p, target); err != nil {
				return err
			}
		default:
			if err := copyRegularFile(root, p, target, perm); err != nil {
				return err
			}
		}

		if fi.IsDir() && options.NoRecursive {
			return iofs.SkipDir
		}
		return nil
	})
	if err != nil {
		return false, errFromOS(err)
	}

	// Deepest directories first, as a directory's members might not be
	// accessible anymore once it has its permissions
	for i := len(dirPerms) - 1; i >= 0; i-- {
		if err := root.Chmod(dirPerms[i].path, dirPerms[i].perm); err != nil {
			return false, errFromOS(err)
		}
	}

	return created, nil
}

func (fs LocalFileSystem) Move(ctx context.Context, src, dst string, options *MoveOptions) (created bool, err error) {
	srcPath, err := fs.relPath(src)
	if err != nil {
		return false, err
	}
	dstPath, err := fs.relPath(dst)
	if err != nil {
		return false, err
	}
	if err := checkStagingPath(srcPath, http.StatusNotFound); err != nil {
		return false, err
	}
	if err := checkStagingPath(dstPath, http.StatusForbidden); err != nil {
		return false, err
	}
	root, err := fs.openRoot()
	if err != nil {
		return false, err
	}
	defer root.Close()

	srcTarget, targetErr := root.Stat(srcPath)
	if targetErr != nil && isPathEscapesError(targetErr) {
		return false, errFromOS(targetErr)
	}
	if _, err := root.Lstat(srcPath); err != nil {
		return false, errFromOS(err)
	}

	// A symbolic link is moved as a link, but one to a collection names
	// that collection, which can't be moved into itself either
	if targetErr == nil && srcTarget.IsDir() && isBeneath(root, srcPath, srcTarget, dstPath) {
		return false, NewHTTPError(http.StatusForbidden, fmt.Errorf("webdav: cannot move a collection into itself"))
	}

	if err := checkParentDir(root, dstPath); err != nil {
		return false, err
	}

	// The destination is removed before moving (RFC 4918 section 9.9.3),
	// which mustn't remove the source. If the destination is the source's
	// entry under another name, as in a case-only rename, it's renamed in
	// place. Otherwise, a destination which is the source, or what the
	// source links to, can only be removed if it's a link of its own, as
	// in Copy: anything else is moving a resource onto itself (RFC 4918
	// section 9.9.4).
	inPlace := isSameDirEntry(root, srcPath, dstPath)
	if !inPlace && isSameFile(root, srcPath, dstPath) && !isSeparateLink(root, srcPath, dstPath) {
		return false, NewHTTPError(http.StatusForbidden, fmt.Errorf("webdav: cannot move a resource onto itself"))
	}

	if _, err := root.Stat(dstPath); err != nil {
		// A symbolic link which doesn't resolve is replaced
		if !isNotExist(err) {
			return false, errFromOS(err)
		}
		if _, lerr := root.Lstat(dstPath); lerr == nil {
			if options.NoOverwrite {
				return false, NewHTTPError(http.StatusPreconditionFailed, os.ErrExist)
			}
			if !inPlace {
				if err := removeAll(root, dstPath); err != nil {
					return false, err
				}
			}
		} else {
			created = true
		}
	} else {
		if options.NoOverwrite {
			return false, NewHTTPError(http.StatusPreconditionFailed, os.ErrExist)
		}
		if containsSource(root, srcPath, dstPath) {
			return false, errOverwriteSourceParent()
		}
		if !inPlace {
			if err := removeAll(root, dstPath); err != nil {
				return false, err
			}
		}
	}

	if err := root.Rename(srcPath, dstPath); err != nil {
		return false, errFromOS(err)
	}

	return created, nil
}
