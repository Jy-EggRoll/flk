package components

type CheckResult struct {
	Type      string
	Device    string
	Path      string
	BasePath  string
	Real      string
	Fake      string
	Prim      string
	Seco      string
	Valid     bool
	Error     string
	ErrorType string
}

type CheckOptions struct {
	DeviceFilters []string
	CheckSymlink  bool
	CheckHardlink bool
	CheckDir      string
}