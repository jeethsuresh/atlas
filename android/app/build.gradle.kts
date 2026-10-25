plugins { id("com.android.application") }
val releaseStore = System.getenv("STORE_KEYSTORE")
android {
 namespace = "cool.gravitas.atlas"
 compileSdk = 36
 defaultConfig {
  applicationId = "cool.gravitas.atlas"
  minSdk = 26
  targetSdk = 36
  versionCode = 2
  versionName = "1.0.1"
 }
 compileOptions { sourceCompatibility = JavaVersion.VERSION_17; targetCompatibility = JavaVersion.VERSION_17 }
 buildFeatures { buildConfig = true }
 signingConfigs {
  if (releaseStore != null) create("release") {
   storeFile = file(releaseStore)
   storePassword = System.getenv("STORE_STORE_PASSWORD")
   keyAlias = System.getenv("STORE_KEY_ALIAS")
   keyPassword = System.getenv("STORE_KEY_PASSWORD")
  }
 }
 buildTypes { release { signingConfig = signingConfigs.findByName("release"); isMinifyEnabled = false } }
 lint { abortOnError = true }
}
val requireReleaseSigning = tasks.register("requireReleaseSigning") {
 doFirst { check(!releaseStore.isNullOrBlank()) { "Release signing configuration is required." } }
}
tasks.matching { it.name == "preReleaseBuild" }.configureEach { dependsOn(requireReleaseSigning) }
dependencies { testImplementation("junit:junit:4.13.2") }
