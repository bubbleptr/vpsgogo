// Package services adapts packages/core and the platform to the views.
// It holds no business rules.
package services

import "runtime"

// AppInfo describes the running app.
type AppInfo struct {
	Version   string
	OS        string
	Arch      string
	GoVersion string
}

// System reports facts about the app and the machine it runs on.
type System struct {
	Version string
}

// Info returns the app version and platform.
func (s System) Info() AppInfo {
	return AppInfo{Version: s.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version()}
}
