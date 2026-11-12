package internal

import "strings"

// SamePath reports whether a and b, two absolute paths or URLs, refer to the
// same resource.
//
// A collection is often referred to both with and without a trailing slash:
// RFC 4918 section 5.2 lets servers handle the URL of a collection without
// the slash as if it had one, and servers differ in the form of the hrefs they
// return. So paths only differing by a trailing slash, such as "/c" and "/c/",
// are the same. Any other difference is significant: "/c//" is neither "/c/"
// nor "/c", "/a//b" isn't "/a/b", and "/c" isn't "/C".
//
// The paths are compared as this module handles them: decoded, as in the Path
// field of url.URL, or as absolute URLs for the resources on another host than
// the endpoint's. Since they're decoded, an escaped slash ("%2F") isn't told
// apart from a slash.
//
// An empty path is neither an absolute path nor a URL: it's only the same as
// another empty path. In particular, it isn't the root "/", although both are
// empty once trimmed of a trailing slash.
func SamePath(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return a == b || strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}
