plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.arnovpn.android"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.arnovpn.android"
        minSdk = 28 // ChaCha20-Poly1305 JCE cipher is available from API 28.
        targetSdk = 35
        versionCode = 1
        versionName = "1.0.0"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
            // A debug signing config is applied so `assembleRelease` in CI
            // produces an installable APK without provisioning secrets. Replace
            // with a real keystore for Play distribution.
            signingConfig = signingConfigs.getByName("debug")
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
