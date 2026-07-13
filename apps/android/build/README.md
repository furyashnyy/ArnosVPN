# apps/android/build

This directory holds the **release APK** for the ArnosVPN Android client:

```
apps/android/build/arnosvpn-release.apk
```

## How the APK gets here

The APK is produced by the `android` GitHub Actions workflow
(`.github/workflows/android.yml`) and committed back to this directory with a
`[skip ci]` commit. GitHub-hosted runners can reach Google's Android package
repositories (`dl.google.com`, the Android SDK, and the Android Gradle Plugin),
so the build runs there and the resulting artifact is checked in alongside the
source.

> The APK is **not** built in Claude's sandbox: that environment's egress
> policy blocks Google's servers, so the Android SDK and Android Gradle Plugin
> cannot be downloaded there. The Kotlin client crypto is still verified in
> that environment against the Go server via pinned cross-language test
> vectors (see `internal/protocol/vectors_test.go` and
> `apps/android/app/src/test/kotlin/.../CryptoTest.kt`).

## Build it yourself

Any machine with the Android SDK and JDK 17 can build the same APK:

```bash
cd apps/android
./gradlew :app:assembleRelease
# output: app/build/outputs/apk/release/app-release.apk
cp app/build/outputs/apk/release/app-release.apk build/arnosvpn-release.apk
```

The release build is signed with the debug key so it installs directly for
testing. Swap in a real keystore in `app/build.gradle.kts` for Play
distribution.
