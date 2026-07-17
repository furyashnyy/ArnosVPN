import groovy.json.JsonSlurper

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Single source of truth for the version: /version.json at the repo root. The
// release workflow reads the same file to tag and publish, so the APK version
// and the GitHub Release tag never drift.
val versionInfo = JsonSlurper()
    .parseText(rootProject.file("../../version.json").readText()) as Map<*, *>
val appVersionName = versionInfo["version"] as String
val appVersionCode = (versionInfo["versionCode"] as Number).toInt()

// Release signing is driven entirely by environment variables supplied from CI
// secrets — no keystore or password is ever committed. CI decodes the keystore
// from the ARNOS_KEYSTORE_BASE64 secret and exports these before assembling.
// When they are absent (local dev builds), the release build falls back to the
// debug signing config so `assembleRelease` still works for testing; such an
// APK is NOT for distribution.
val releaseKeystore = System.getenv("ARNOS_KEYSTORE_FILE")
val releaseStorePass = System.getenv("ARNOS_KEYSTORE_PASSWORD")
val releaseKeyAlias = System.getenv("ARNOS_KEY_ALIAS")
// The release keystore is PKCS12, where the private key is protected by the
// store password itself (a separate key password isn't supported). We therefore
// always sign with the store password and ignore any ARNOS_KEY_PASSWORD secret,
// which removes a common footgun: the two password secrets drifting apart and
// breaking signing ("keystore password was incorrect" / "final block not
// properly padded"). Only ARNOS_KEYSTORE_PASSWORD needs to be correct.
val haveReleaseSigning = !releaseKeystore.isNullOrBlank() &&
    file(releaseKeystore).exists() &&
    !releaseStorePass.isNullOrBlank() &&
    !releaseKeyAlias.isNullOrBlank()

android {
    namespace = "com.arnosvpn.android"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.arnosvpn.android"
        minSdk = 28 // ChaCha20-Poly1305 JCE cipher is available from API 28.
        targetSdk = 35
        versionCode = appVersionCode
        versionName = appVersionName
    }

    signingConfigs {
        // The stable release key is supplied only via CI secrets (never committed).
        // Using one fixed key for every build is what lets the app UPDATE in
        // place; a per-run debug key would make differently-signed APKs fail with
        // "conflicts with another package".
        if (haveReleaseSigning) {
            create("release") {
                storeFile = file(releaseKeystore!!)
                storePassword = releaseStorePass!!
                keyAlias = releaseKeyAlias!!
                keyPassword = releaseStorePass!! // PKCS12: key password == store password
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
            // Real release key when CI provides it; otherwise the debug key so
            // local `assembleRelease` still produces an installable (non-store) APK.
            signingConfig = if (haveReleaseSigning) {
                signingConfigs.getByName("release")
            } else {
                signingConfigs.getByName("debug")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        viewBinding = true
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.constraintlayout:constraintlayout:2.1.4")
    implementation("androidx.lifecycle:lifecycle-service:2.8.7")
    implementation("androidx.activity:activity-ktx:1.9.3")

    // WebSocket transport (the HTTPS-camouflaged tunnel carrier).
    implementation("com.squareup.okhttp3:okhttp:4.12.0")

    // QR scanning for one-scan provisioning.
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")

    testImplementation("junit:junit:4.13.2")
}
