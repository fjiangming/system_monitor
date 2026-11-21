# Third-party software

The embedded Lucide icons are lucide 0.468.0, distributed under the ISC license.
The original license is included at `web/lucide-LICENSE.txt`; the vendored UMD
bundle is included at `web/lucide.min.js`. The program makes no runtime CDN calls.

The Go dependencies and exact transitive versions are recorded in `go.mod` and
`go.sum`. SQLite is provided by modernc.org/sqlite (BSD-3-Clause); SQLite itself
is public domain. Each dependency retains its upstream license. Development
browser checks use Playwright (Apache-2.0), recorded in the npm lockfile.
