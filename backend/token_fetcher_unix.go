//go:build linux || darwin

package backend

func tokenHelperName() string {
	return "get_token"
}
