package version

// Version is the qodercn-gateway build version. It is injected at build time via
//
//	-ldflags "-X qodercn-gateway/internal/version.Version=$(cat VERSION)"
//
// (see the Dockerfile and .github/workflows/release.yml). A plain `go build` /
// `go run` without that flag reports "dev". The VERSION file is the single source.
var Version = "dev"
