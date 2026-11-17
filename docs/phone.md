# Install on your phone

PagerArchive can go on your phone's home screen and open like an app, full screen, without the browser's bar. It's still the same site: nothing to download from an app store, and it's always the version your server runs.

## Android (Chrome)

1. Open your PagerArchive address in Chrome and sign in.
2. Tap Chrome's menu (⋮), then **Install app** (on some phones, **Add to home screen**, then **Install**).
3. Open PagerArchive from the home screen or the app drawer.

## iPhone and iPad (Safari)

1. Open your PagerArchive address in Safari.
2. Tap **Share** (the square with an arrow), then **Add to Home Screen**, then **Add**.
3. Open PagerArchive from the home screen and sign in. An app on the home screen keeps its own sign-in, apart from Safari's, so you sign in once there too.

## Good to know

- **Signing in** takes you to the sign-in page on its own address and back. On Android it opens over the app with a bar showing its address; on iPhone, inside the app.
- **Signing out** signs you out of PagerArchive and of the sign-in service on that phone, and comes back to PagerArchive's sign-in page, so opening the app again asks you to sign in. Anything else on that phone's browser that signs in through the same sign-in service asks again too. If the sign-in service can't be reached just then, the page says so: you're signed out of PagerArchive, but the next sign-in may not ask for your password.
- **Updates** come by themselves: when your server is upgraded, the app loads the new version the next time you open a page in it.
- **Offline**, the app says "You're offline" and shows nothing else. PagerArchive keeps nothing from the archive on your phone: only the app itself (its code and icons).
- **No notifications.** The app never sends notifications or asks to. PagerArchive isn't an alerting service and doesn't replace an issued pager. To have pages sent somewhere, use [integrations](integrations.md).

For whoever runs the server: installing needs HTTPS, which the reverse proxy gives ([deploying](deploy.md#reverse-proxy)).
