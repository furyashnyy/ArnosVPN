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

The APK is signed with a **stable release key that is never committed to the
repository**. CI reads it from GitHub Actions **secrets**, decodes the keystore
at build time, and signs with it; because every build uses the same key, the app
**updates in place**. Local `assembleRelease` builds (no secrets) fall back to
the debug key and are **not for distribution**.

### One-time setup: generate a key and add the secrets

Generate the keystore **on your own machine** (the private key must never pass
through CI logs, chat, or the repo):

```bash
keytool -genkeypair -v \
  -keystore arnos-release.jks -alias arnos \
  -keyalg RSA -keysize 4096 -validity 10000 \
  -storepass '<STRONG_STORE_PASSWORD>' -keypass '<STRONG_KEY_PASSWORD>' \
  -dname "CN=ArnosVPN"
base64 -w0 arnos-release.jks   # copy this into the ARNOS_KEYSTORE_BASE64 secret
```

Then add four **repository secrets** (Settings → Secrets and variables →
Actions):

| Secret | Value |
|--------|-------|
| `ARNOS_KEYSTORE_BASE64` | base64 of `arnos-release.jks` (from above) |
| `ARNOS_KEYSTORE_PASSWORD` | the store password |
| `ARNOS_KEY_ALIAS` | `arnos` |
| `ARNOS_KEY_PASSWORD` | the key password |

Keep `arnos-release.jks` and the passwords in a safe place (a password manager);
losing them means you can no longer ship in-place updates. The `release`
workflow **fails** if `ARNOS_KEYSTORE_BASE64` is unset, so a release is never
published unsigned; the `android` workflow builds a debug-signed APK for testing
but does not commit it back unless the secret is set.

> **⚠️ The previously committed key is compromised.** Earlier history contained
> `apps/android/release.jks` with a known password, so that key must be
> considered public. Generate a **fresh** key as above, and purge the old one
> from history (it is already removed from the working tree):
>
> ```bash
> # from a fresh clone; this rewrites history — coordinate before force-pushing
> git filter-repo --path apps/android/release.jks --invert-paths
> git push --force-with-lease origin main
> ```
>
> After switching keys, existing side-loaded installs are signed with the old
> key. **Uninstall the old ArnosVPN once**, then install the new signed APK; all
> future updates install cleanly.

> **Play Protect** may warn "unknown developer" for any side-loaded app — tap
> **Install anyway**. This is expected for apps not distributed via the Play
> Store and is unrelated to the app's safety.

## Artifacts

Per-run artifacts (uploaded by the `android`/`desktop` workflows for
convenience) are pruned weekly by the **`cleanup-artifacts`** workflow
(default: older than 7 days). Release assets on the Releases page are permanent
and never touched by cleanup.
