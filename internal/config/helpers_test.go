package config_test

import "strings"

const (
	testHome     = "/home/ana"
	testOS       = "linux"
	testFile     = "config.json"
	testNickname = "Qwen3.8 27B"
)

// slash normalises both path flavours, because filepath.ToSlash only converts the
// separator of the system the test is running on: a Windows layout resolved on
// Linux keeps its backslashes.
func slash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}
