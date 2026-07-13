# Releasing ArnosVPN

Releases are automated. The version lives in one place — **`version.json`** at
the repo root — and everything derives from it.

```json
{ "version": "1.0.0", "versionCode": 1 }
```

- `version` → the Android `versionName`, the desktop build, and the Git tag
  `v<version>`.
- `versionCode` → the Android `versionCode` (must **increase** for every
  Play/side-load update).

## Cut a release

1. Bump the version:

   ```bash
   # e.g. 1.0.0 -> 1.1.0
   $EDITOR version.json      # set "version" and bump "versionCode"
   git add version.json
   git commit -m "release: v1.1.0"
   git push origin main
   ```

2. That push triggers the **`release`** workflow, which:
   - builds the **signed** Android APK,
   - cross-builds the **Windows** and **Linux** desktop apps,
   - creates the GitHub Release **`v1.1.0`** and uploads:
     - `arnosvpn-1.1.0.apk`
     - `arnosvpn-client-windows-amd64.exe` (+ `wintun.dll` for TUN mode)
     - `arnosvpn-client-linux-amd64`

   Find it under the repo's **Releases** page.

The workflow is idempotent: if the tag already exists it does nothing, so a
release only happens when you actually bump the version. You can also run it
manually from the Actions tab (**release → Run workflow**).

## Signing

The APK is signed with a **stable release key** committed at
`apps/android/release.jks` (alias `arnos`). Because every build uses the same
key, the app **updates in place**. Default passwords are baked in for a private,
single-user setup; to use your own, set repo secrets and pass them as env:

```
ARNOS_KEYSTORE_PASSWORD, ARNOS_KEY_ALIAS, ARNOS_KEY_PASSWORD
```

> **First install after switching keys:** older side-loaded builds were signed
> with CI's throwaway debug key, so Android refuses to update over them
> ("conflicts with another package"). **Uninstall the old ArnosVPN once**, then
> install the new signed APK. All future updates install cleanly.

> **Play Protect** may warn "unknown developer" for any side-loaded app — tap
> **Install anyway**. This is expected for apps not distributed via the Play
> Store and is unrelated to the app's safety.

## Artifacts

Per-run artifacts (uploaded by the `android`/`desktop` workflows for
convenience) are pruned weekly by the **`cleanup-artifacts`** workflow
(default: older than 7 days). Release assets on the Releases page are permanent
and never touched by cleanup.
