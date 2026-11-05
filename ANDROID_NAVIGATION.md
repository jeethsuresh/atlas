# Gravitas Android navigation

This is the standing navigation contract for current and future Gravitas apps.
On Android 14 and later, an inward left-edge system gesture navigates Back; an
inward right-edge system gesture navigates Forward. Forward restores the last
browsing destination left by Back and does nothing when there is no forward
history. Hardware and three-button Back remain Back. Android 13 and earlier keep
platform Back behavior: their committed Back callback does not identify the edge.
Do not guess the edge from timing or require root, accessibility access, or global
gesture exclusions.

A new destination after Back discards the abandoned forward branch. Maintain a
bounded, in-memory history of destinations, separate from application data. Restore
view state where appropriate, and validate that a referenced item still exists and
is accessible. Clear history on logout, account/server changes, and loss of access.
Forward must not repeat sends, installs, deletions, login, permission requests,
sharing consent, or call initiation. Unsaved editors retain their normal discard
confirmation. Keyboard dismissal takes priority over navigation; dialogs consume
Back and block Forward. Cancelled gestures change nothing. History is local to an
app; it never reopens another app or crosses an authentication boundary.

## Shared implementation

Canonical sources live in Private Store's `integrations/navigation/`:
https://github.com/jeethsuresh/appstore/tree/main/integrations/navigation

- `GravitasEdgeNavigation.java`: native, side-aware committed system gesture bridge.
- `NavigationHistory.kt`: bounded history for native destinations.
- `GravitasFlutterActivity.kt`: Flutter activity host, including cached engines.
- `gravitas_navigation.dart`: one forward stack spanning route and state navigation.
- `gravitas_navigation_test.dart`: routing regression tests to copy into each client.

From a Private Store checkout, run `python3 integrations/navigation/install-flutter.py /path/to/client` to copy Flutter components and tests; then integrate the routing as below.

Use `android:enableOnBackInvokedCallback="true"`. The native helper handles only
API 34+, preserving the supported legacy behavior below that API. Flutter hosts
extend `GravitasFlutterActivity`; MaterialApp supplies `GravitasNavigationObserver`
and wraps its navigator in `GravitasNavigation`. Mark browsing pages with
`HistoryPageRoute` and browsing sheets with `HistoryDestination`. Wrap state-based
screens in `HistoryBoundary` using immutable destination values, and key that
boundary by account identity. Do not record typing, loading, playback, or refresh
updates as navigation. Keep canonical copies synchronized when changing shared
behavior. TV/remote interfaces retain their directional controls; edge gestures
apply to touch interfaces.

## Required validation

Test left Back, right Forward, empty Forward, cancellation, three-button/hardware
Back, nested pages, multiple Back/Forward steps, new branches, item removal, modal
and keyboard precedence, and account isolation. Exercise the actual OS gesture on
an Android 14+ emulator/device as well as Flutter/widget or native unit tests.
Run each app's pinned analysis, tests, Android build and lint, then wait for its
required CI on the final PR commit before merging and publishing a new release.
New screens and future apps must preserve this contract.
