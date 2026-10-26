# ftransport x/net snapshot

This directory is a reduced source snapshot of `golang.org/x/net v0.61.0`
(commit `c1d31df183e51b1075a2fd670d6f6f4efb871773`). It contains only packages in
the ftransport HTTP/2 build closure plus the `proxy` and `publicsuffix`
packages required by its first in-repository consumer. `MANIFEST.sha256`
records the upstream and local SHA-256 for every copied file. The original
module checksum is recorded in the repository `SOURCES.md`.

The production copy lives under `internal/xnet`, where imports are rewritten to
the ftransport module path so downstream modules do not depend on a
non-transitive `replace` directive. This directory remains the byte-for-byte
provenance snapshot. Original copyright headers, `LICENSE`, and `PATENTS` are
preserved.

Local changes are restricted to `http2`:

- select the auditable legacy implementation on Go 1.27 and later instead of
  the unexported standard-library wrapper;
- accept an exact ordered initial SETTINGS list, including explicit empty and
  zero values;
- keep stream and connection receive-window accounting equal to advertised
  SETTINGS and WINDOW_UPDATE values;
- emit ordered initial PRIORITY frames and optional HEADERS priority, including
  an all-zero wire priority tuple;
- order pseudo and regular request fields before HPACK encoding and mark
  request-selected fields as never indexed;
- distinguish an explicit zero MAX_HEADER_LIST_SIZE from the upstream
  zero-means-default API convention;
- cap pooled connections per authority and let each excess waiter cancel with
  its own request context while waiting for any stream slot to become free.
- retain dial cancellation through the HTTP/2 preface and initial frame writes;
  close an abandoned connection before retrying for a live pool waiter.

To update the snapshot, download the pinned replacement version, copy the same
package closure without test files (including `proxy`, `publicsuffix`, and
`internal/socks`), replay the small changes above, regenerate
`MANIFEST.sha256`, copy the production closure into `internal/xnet`, rewrite
its internal import prefix, run both toolchain suites, and review upstream HTTP/2 security,
flow-control, parser, HPACK, retry, and shutdown changes before accepting it.
