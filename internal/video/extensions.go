package video

import "strings"

func IsVideoExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m2ts", ".m4v", ".wmv", ".ts", ".webm":
		return true
	default:
		return false
	}
}
