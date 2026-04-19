package pathguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolveExistingUnderRoot(root, target string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	target = filepath.Clean(strings.TrimSpace(target))
	if root == "" || target == "" {
		return "", fmt.Errorf("invalid path input")
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	if !WithinRoot(resolvedTarget, resolvedRoot) {
		return "", fmt.Errorf("path is outside root")
	}
	return resolvedTarget, nil
}

func WithinRoot(target, root string) bool {
	target = filepath.Clean(strings.TrimSpace(target))
	root = filepath.Clean(strings.TrimSpace(root))
	if target == "" || root == "" {
		return false
	}
	if target == root {
		return true
	}
	return strings.HasPrefix(target+string(os.PathSeparator), root+string(os.PathSeparator))
}
