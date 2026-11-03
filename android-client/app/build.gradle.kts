import com.android.build.api.variant.ResValue

plugins { id("com.android.application"); id("org.jetbrains.kotlin.android") }
android {
    namespace = "io.worktool.open.prototype"
    compileSdk = 36
    buildToolsVersion = "35.0.0"
    defaultConfig {
        applicationId = "io.worktool.open.prototype"
        minSdk = 26
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0-native-alpha"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }
    compileOptions { sourceCompatibility = JavaVersion.VERSION_17; targetCompatibility = JavaVersion.VERSION_17 }
    kotlinOptions { jvmTarget = "17" }
    buildTypes {
        release { isMinifyEnabled = false }
        create("lab") {
            initWith(getByName("debug"))
            applicationIdSuffix = ".lab"
            versionNameSuffix = "-LAB-CLEARTEXT"
            matchingFallbacks += listOf("debug")
        }
    }
    buildFeatures { resValues = true }
    testOptions { unitTests.isReturnDefaultValues = false }
    sourceSets.getByName("test").resources.srcDir("../../contracts/interop")
}
dependencies {
    implementation("com.squareup.okhttp3:okhttp:5.3.2")
    implementation("io.github.erdtman:java-json-canonicalization:1.1")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20250517")
    androidTestImplementation("androidx.test:core:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("androidx.test:runner:1.7.0")
}

// Final installed package for exact event filtering; suffix is never guessed.
androidComponents {
    onVariants(selector().all()) { variant ->
        variant.resValues.put(
            variant.makeResValueKey("string", "ui_accessibility_package_filter"),
            variant.applicationId.map { ResValue(it, "Exact installed applicationId for own-package event filtering") }
        )
    }
}
