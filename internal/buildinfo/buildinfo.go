// Package buildinfo carries the version stamped into the binary at build
// time. The Docker build passes -ldflags "-X ticketd/internal/buildinfo.Version=…"
// with the value of `git describe --tags`, so a released container always
// reports the exact tag it was built from. Development builds keep "dev",
// which the UI treats as "no version to show".
package buildinfo

// Version is the build's version string ("dev" unless overridden).
var Version = "dev"
