package backend

import (
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func GetOSInfo() (string, error) {
	arch := runtime.GOARCH

	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return fmt.Sprintf("Windows %s", arch), nil
	}
	defer key.Close()

	productName := readRegistryString(key, "ProductName")
	if productName == "" {
		productName = "Windows"
	}

	version := readWindowsVersion(key)
	build := readRegistryString(key, "CurrentBuildNumber")
	if version != "" && build != "" {
		return fmt.Sprintf("%s (%s.%s, %s)", productName, version, build, arch), nil
	}
	if build != "" {
		return fmt.Sprintf("%s (build %s, %s)", productName, build, arch), nil
	}
	return fmt.Sprintf("%s (%s)", productName, arch), nil
}

func readRegistryString(key registry.Key, name string) string {
	value, _, err := key.GetStringValue(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func readWindowsVersion(key registry.Key) string {
	major, _, majorErr := key.GetIntegerValue("CurrentMajorVersionNumber")
	minor, _, minorErr := key.GetIntegerValue("CurrentMinorVersionNumber")
	if majorErr == nil && minorErr == nil {
		return fmt.Sprintf("%d.%d", major, minor)
	}

	version := readRegistryString(key, "CurrentVersion")
	if version != "" {
		return version
	}
	return ""
}
