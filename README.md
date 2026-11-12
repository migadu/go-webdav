# go-webdav

[![Go Reference](https://pkg.go.dev/badge/github.com/migadu/go-webdav.svg)](https://pkg.go.dev/github.com/migadu/go-webdav)

A Go library for building [WebDAV], [CalDAV] and [CardDAV] clients and servers.

This is an **independent hard fork** of
[emersion/go-webdav](https://github.com/emersion/go-webdav), maintained by
[Migadu](https://github.com/migadu). This fork uses the module path
`github.com/migadu/go-webdav`, with its own releases and issue tracker.

The original library was created by **Simon Ser (emersion)**. We thank Simon and
all upstream contributors for their work and the foundation this fork builds on.
The original copyright and MIT license are preserved.

## Packages and capabilities

| Package | Purpose |
| --- | --- |
| [`webdav`](https://pkg.go.dev/github.com/migadu/go-webdav) | File and collection operations, HTTP handlers, a local filesystem backend, properties and optional locking. |
| [`caldav`](https://pkg.go.dev/github.com/migadu/go-webdav/caldav) | Calendar discovery, calendar and object operations, queries, multiget and synchronization. |
| [`carddav`](https://pkg.go.dev/github.com/migadu/go-webdav/carddav) | Address book discovery, address book and contact operations, queries, multiget and synchronization. |

Clients accept a custom HTTP client, use contexts for cancellation, and support
HTTP basic authentication through a helper. CalDAV and CardDAV include DNS
service discovery, principal and home-set discovery, and conditional writes
using ETags. Calendar and contact data use
[`go-ical`](https://github.com/emersion/go-ical) and
[`go-vcard`](https://github.com/emersion/go-vcard).

Server handlers implement `net/http.Handler` and use application-supplied backend
interfaces. Optional interfaces add collection property updates, synchronization,
batch retrieval and WebDAV-Push subscriptions. WebDAV also offers optional dead
property storage and locking through `DeadPropertyFileSystem` and `LockSystem`.
The API documentation describes the requirements and capabilities of each
interface.

## Installation

Requires **Go 1.25 or later**.

```sh
go get github.com/migadu/go-webdav
```

Import the packages from this fork:

```go
import (
	"github.com/migadu/go-webdav"
	"github.com/migadu/go-webdav/caldav"
	"github.com/migadu/go-webdav/carddav"
)
```

When migrating from upstream, update the `github.com/emersion/go-webdav` import
prefix throughout your application and review the API documentation for the
version you adopt.

## Client example

List the immediate children of a WebDAV collection. Set `WEBDAV_URL`,
`WEBDAV_USERNAME` and `WEBDAV_PASSWORD` for your server; for example,
`WEBDAV_URL=https://dav.example.com/`.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/migadu/go-webdav"
)

func main() {
	httpClient := webdav.HTTPClientWithBasicAuth(nil,
		os.Getenv("WEBDAV_USERNAME"), os.Getenv("WEBDAV_PASSWORD"))
	client, err := webdav.NewClient(httpClient, os.Getenv("WEBDAV_URL"))
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	files, err := client.ReadDir(ctx, "", false)
	if err != nil {
		log.Fatal(err)
	}
	for _, file := range files {
		fmt.Println(file.Path)
	}
}
```

Use `caldav.NewClient` or `carddav.NewClient` with the same HTTP client helper for
calendars or contacts. Their embedded WebDAV client also provides shared
operations such as principal discovery and resource deletion.

## Building a server

Use `webdav.Handler` with a `webdav.FileSystem`, `caldav.Handler` with a
`caldav.Backend`, or `carddav.Handler` with a `carddav.Backend`. Your application
provides storage, authentication and authorization. Implement the optional
backend interfaces for the extensions your service supports.

A local filesystem server is included for development:

```sh
go run ./cmd/webdav-server -addr 127.0.0.1:8080 -locking /path/to/files
```

See the [example command](cmd/webdav-server/main.go) and the package documentation
for handler configuration and backend contracts.

## Development and contributions

Report bugs and propose changes in the
[Migadu issue tracker](https://github.com/migadu/go-webdav/issues). Include a
reproducer for protocol or interoperability problems; regression tests are
welcome with fixes.

Run the checks used by CI:

```sh
gofmt -l .
go vet -composites=false ./...
go test -race ./...
```

The Docker-based [litmus runner](contrib/litmus.sh) exercises WebDAV conformance
against the local filesystem server. See the script for the suites it runs,
expected exceptions and Docker Desktop usage.

## License

Released under the [MIT license](LICENSE). The original copyright notice for
Simon Ser is retained.

[WebDAV]: https://tools.ietf.org/html/rfc4918
[CalDAV]: https://tools.ietf.org/html/rfc4791
[CardDAV]: https://tools.ietf.org/html/rfc6352
