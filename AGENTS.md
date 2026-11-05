# Atlas

The backend is Go; `web/` is shared by browsers and the Android WebView. Keep location sharing opt-in, foreground-only and limited to accepted friends. Never expose another user's lists, invitation tokens or stale coordinates. Do not log coordinates, credentials or auth callback URLs.

Run `go test -race ./...`, `go vet ./...`, `node --check web/app.js`, and the release helper tests before shipping. Android code changes also require `./gradlew --no-daemon assembleDebug testDebugUnitTest lintDebug` with JDK 17. Review final diffs and pass CI before merging PRs.

Successful pushes to main automatically deploy the tested immutable Go/web image through the namespace-scoped Gravitas workflow. Verify the image digest, rollout and public HTTPS. Android releases explicitly increase versionName and versionCode, update changelog.json, and publish a signed APK, manifest and checksums automatically through the main-only android-release environment. Never change an existing signing identity or overwrite release assets. Verify anonymous download access and live Private Store ingestion.

## Android back and forward navigation

All current and future Gravitas Android touch interfaces must follow [ANDROID_NAVIGATION.md](ANDROID_NAVIGATION.md): left-edge Back and right-edge Forward on Android 14+, bounded destination history, safe restoration without repeating actions, keyboard/modal precedence, and account isolation. New screens must integrate the shared history and include relevant regression coverage. Keep legacy Back behavior on older Android versions.
