package webdav

import (
	"context"
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

	"github.com/emersion/go-webdav/internal"
)

// LocalFileSystem implements FileSystem for a local directory.
//
// Accesses are confined to the directory with os.Root: symbolic links are
// only followed if they are relative and resolve to a location inside the
// directory.
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

	if errors.Is(err, iofs.ErrNotExist) {
		return NewHTTPError(http.StatusNotFound, err)
	} else if errors.Is(err, iofs.ErrPermission) || isPathEscapesError(err) {
		return NewHTTPError(http.StatusForbidden, err)
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		return NewHTTPError(http.StatusServiceUnavailable, err)
	} else {
		return err
	}
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
	root, err := fs.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var l []FileInfo
	err = iofs.WalkDir(root.FS(), dir, func(p string, d iofs.DirEntry, err error) error {
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

		l = append(l, *fileInfoFromOS("/"+p, fi))

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
		if ok, err := ifNoneMatch.MatchETag(etag); err != nil {
			return NewHTTPError(http.StatusBadRequest, err)
		} else if ok {
			return NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("If-None-Match condition failed"))
		}
	}

	return nil
}

func (fs LocalFileSystem) Create(ctx context.Context, name string, body io.ReadCloser, opts *CreateOptions) (fi *FileInfo, created bool, err error) {
	p, err := fs.relPath(name)
	if err != nil {
		return nil, false, err
	}
	root, err := fs.openRoot()
	if err != nil {
		return nil, false, err
	}
	defer root.Close()

	fi, _ = statRoot(root, p, name)
	created = fi == nil

	if err := checkConditionalMatches(fi, opts.IfMatch, opts.IfNoneMatch); err != nil {
		return nil, false, err
	}

	wc, err := root.Create(p)
	if err != nil {
		return nil, false, errFromOS(err)
	}
	defer wc.Close()

	if _, err := io.Copy(wc, body); err != nil {
		root.Remove(p)
		return nil, false, err
	}
	if err := wc.Close(); err != nil {
		root.Remove(p)
		return nil, false, err
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
	root, err := fs.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()

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
	if os.IsNotExist(err) {
		return NewHTTPError(http.StatusConflict, err)
	} else if err != nil {
		return errFromOS(err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return dstFile.Close()
}

// isSubPath reports whether p is located beneath dir. Both paths are cleaned
// and relative to the root directory.
func isSubPath(dir, p string) bool {
	if dir == "." {
		return p != "."
	}
	return strings.HasPrefix(p, dir+"/")
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
	// infinite recursion if not handled correctly" (RFC 4918 section 9.8.3)
	if srcInfo.IsDir() && !options.NoRecursive && isSubPath(srcPath, dstPath) {
		return false, NewHTTPError(http.StatusForbidden, fmt.Errorf("webdav: cannot copy a collection into itself"))
	}

	if _, err := root.Stat(dstPath); err != nil {
		if !os.IsNotExist(err) {
			return false, errFromOS(err)
		}
		created = true
	} else {
		if options.NoOverwrite {
			return false, NewHTTPError(http.StatusPreconditionFailed, os.ErrExist)
		}
		if err := removeAll(root, dstPath); err != nil {
			return false, err
		}
	}

	err = iofs.WalkDir(root.FS(), srcPath, func(p string, d iofs.DirEntry, err error) error {
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

		if fi.IsDir() {
			if err := root.Mkdir(target, perm); err != nil {
				return errFromOS(err)
			}
		} else {
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
	root, err := fs.openRoot()
	if err != nil {
		return false, err
	}
	defer root.Close()

	if _, err := root.Stat(dstPath); err != nil {
		if !os.IsNotExist(err) {
			return false, errFromOS(err)
		}
		created = true
	} else {
		if options.NoOverwrite {
			return false, NewHTTPError(http.StatusPreconditionFailed, os.ErrExist)
		}
		if err := removeAll(root, dstPath); err != nil {
			return false, err
		}
	}

	if err := root.Rename(srcPath, dstPath); err != nil {
		return false, errFromOS(err)
	}

	return created, nil
}
