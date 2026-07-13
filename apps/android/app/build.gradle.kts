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
        // Stable release key (apps/android/release.jks). Using one fixed key for
        // every build is what lets the app UPDATE in place — CI's debug key is
        // regenerated each run, which is why differently-signed APKs failed with
        // "conflicts with another package".
        create("release") {
            storeFile = file("../release.jks")
            storePassword = System.getenv("ARNOS_KEYSTORE_PASSWORD") ?: "arnosvpn"
            keyAlias = System.getenv("ARNOS_KEY_ALIAS") ?: "arnos"
            keyPassword = System.getenv("ARNOS_KEY_PASSWORD") ?: "arnosvpn"
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
            signingConfig = signingConfigs.getByName("release")
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
