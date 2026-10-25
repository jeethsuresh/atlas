# Atlas

Your places, your paths, and your people on one OpenStreetMap map.

- **Web:** https://maps.gravitas.cool
- **Android:** https://apps.gravitas.cool/apps/atlas (Android 8.0+)
- **Signed releases:** https://github.com/jeethsuresh/atlas/releases

Toggle nearby businesses, saved lists, routes/street highlights and friends independently. Search places and addresses, save a business into multiple lists, rename collections and remove pins. Plan driving directions, save a route, highlight a named street within 5 km or draw a personal path. Business data is community maintained and may be incomplete. Public transit schedules, live traffic, offline map packs and background sharing are not included.

Explore without an account; guest lists and highlights stay on that browser/device. Sign in with Gravitas to sync the map on web and Android. A new account imports this device's guest map only when the server has no saved state. Later changes use revision checks; reload on a conflict to load the server copy. Unsynced authenticated edits are held in memory and are not persisted after reload.

Friends connect through single-use, 24-hour invitations. Accepting connects both users but does not enable sharing. Sharing needs explicit consent and foreground location permission, pauses on hiding/switching apps, and expires within two minutes after the last update. The server stores only the latest location, never location history. Removing a friend ends access in both directions. Device permissions can be revoked at any time. A retained disk is not an offsite backup.

## Run and check

Requires Go 1.26.2 and a C compiler for SQLite and race tests. No Node build or frontend package installation is needed; Leaflet 1.9.4 is vendored with its license.

```sh
go test -race ./...
go vet ./...
go run ./cmd/atlas
```

The local app serves at http://localhost:8080. `DB_PATH` defaults to `atlas.db`. Without `GRAVITAS_SESSION_KEY` it runs in guest mode and protected endpoints return 401. Production requires `APP_URL=https://maps.gravitas.cool` and a persistent 32-byte base64url session encryption key. Cookies are Secure/HttpOnly and bound to that origin, authorization uses PKCE, mutations require a CSRF header, and authenticated requests recheck central session status. Only accepted friends can query locations. Never log coordinates or authentication callback URLs.

Maps use OpenStreetMap tiles; address searches use Nominatim (explicit submit, serialized to at most one request per second); nearby businesses and streets use Overpass; driving directions use OSRM. The Go proxy bounds concurrency, request sizes, geographic ranges and cached responses. These public services have no availability guarantee. External providers receive the requested area or query; no friend coordinates are sent to them by the sharing service. No tiles are prefetched or cached by the service worker. Attribution stays visible. [Report a map issue](https://www.openstreetmap.org/fixthemap) or [contact the maintainer](https://github.com/jeethsuresh/atlas/issues).

Android uses the same hosted frontend inside a native Java WebView with pinned first-party navigation, HTTPS-only networking, foreground location permission, no file access or JavaScript bridge, disabled backups, and external links opened in the browser. The app requires a network connection for maps and sign-in. Backend/frontend fixes reach both platforms together. An Android SDK/permission/launcher change requires a new signed APK.

## Automatic releases

Every push to `main` runs backend race tests and vet in the production toolchain, checks JavaScript syntax and release helpers, and builds/tests/lints Android. After successful checks, GitHub publishes the Go/web image and deploys its immutable digest through private Gravitas tooling using namespace-only credentials. Rollout and public HTTPS health must pass. Runtime state uses a retained 5 GiB PVC; deployment has a read-only root and runs as UID 10001.

Android publishing uses the main-only `android-release` environment and a permanent certificate pinned in `android/release-certificate.sha256`. Increase `versionName` and `versionCode` in `android/app/build.gradle.kts` and update `changelog.json` for every Android version. Main automatically tests, signs, verifies and publishes the APK, changelog, manifest and SHA256SUMS as a stable GitHub release. Existing versions are skipped; existing tags/assets are never overwritten. Private Store polls releases every 15 minutes. Signing credentials and runtime keys never belong in Git.

The web deployment uses `.gravitas/deploy.json`; bootstrap access is provisioned once by the existing private `jeethsuresh/gravitas-deploy` tool. Source and container are public; user records and friend coordinates require account authorization.
