package version

var Version = "dev"
var Commit = "unknown"

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func Current() Info {
	return Info{Version: Version, Commit: Commit}
}
