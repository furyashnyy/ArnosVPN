# OkHttp / Okio ship their own rules; keep our protocol classes intact.
-keep class com.arnovpn.android.protocol.** { *; }
-dontwarn okhttp3.**
-dontwarn okio.**
