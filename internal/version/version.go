// Package version holds the build version, injected at build time with
//
//	go build -ldflags "-X onefrog/internal/version.Version=v0.1.1"
//
// Builds without it report "dev" and never show update notices.
package version

// Version is the release tag this binary was built from, or "dev".
var Version = "dev"
