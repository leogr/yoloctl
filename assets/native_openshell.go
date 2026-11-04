package assets

// Committed default: no native selection. make build (native-qualify) writes
// the qualified selection next to the embedded archives, in the git-ignored
// internal/nativeartifacts/embedded/selection/, and the loader prefers it.
// Runtime loading checks the manifest hash; it never derives a new trust hash
// or uses a fixture. Historical runtimes retain their own recorded
// qualification and helper pair.
const (
	nativeOpenShellManifest       = ""
	nativeOpenShellManifestSHA256 = ""
	nativeOpenShellRego           = ""
)

// NativeOpenShellQualification returns independent copies of the compiled
// native release selection. Empty values mean no production release is selected;
// they never authorize a fixture, automatic refresh or legacy fallback.
func NativeOpenShellQualification() (manifest []byte, reviewedHash string, rego []byte) {
	return []byte(nativeOpenShellManifest), nativeOpenShellManifestSHA256, []byte(nativeOpenShellRego)
}
