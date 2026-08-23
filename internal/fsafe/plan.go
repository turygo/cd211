package fsafe

import (
	"fmt"
	"os"
	"path/filepath"
)

// FilePlan describes one effective manifest operation inside a verified torrent root.
type FilePlan struct {
	Index         int64
	OriginalPath  string
	EffectivePath string
	Priority      int64
	Size          int64
}

// ApplyFilePlan applies all renames in two phases and converges after a
// crash between staging and publication. Every path component is checked
// without following symlinks.
func (v *Verifier) ApplyFilePlan(root, hash string, plans []FilePlan) error {
	if hash == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("fsafe: invalid file plan root")
	}
	evaluated, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	evaluated = filepath.Clean(evaluated)
	if !strictlyWithin(v.localRoot, evaluated) {
		return fmt.Errorf("fsafe: file plan escapes local root")
	}
	moving := make(map[string]FilePlan)
	temps := make(map[string]string)
	targetSources := make(map[string]string)
	completed := make(map[string]bool)
	for _, plan := range plans {
		if !validManifestPath(plan.OriginalPath) || !validManifestPath(plan.EffectivePath) || plan.Size < 0 || (plan.Priority != 0 && plan.Priority != 1 && plan.Priority != 6 && plan.Priority != 7) {
			return fmt.Errorf("fsafe: invalid file plan")
		}
		if err := safePlanComponents(evaluated, plan.OriginalPath); err != nil {
			return err
		}
		if err := safePlanComponents(evaluated, plan.EffectivePath); err != nil {
			return err
		}
		if plan.Priority == 0 || filepath.Clean(plan.OriginalPath) == filepath.Clean(plan.EffectivePath) {
			continue
		}
		source := filepath.Join(evaluated, filepath.FromSlash(plan.OriginalPath))
		if _, exists := moving[source]; exists {
			return fmt.Errorf("fsafe: duplicate file plan source")
		}
		moving[source] = plan
		temps[source] = filepath.Join(evaluated, fmt.Sprintf(".cd211-plan-%s-%d", hash, plan.Index))
		targetSources[filepath.Join(evaluated, filepath.FromSlash(plan.EffectivePath))] = source
	}
	for source, plan := range moving {
		temp, target := temps[source], filepath.Join(evaluated, filepath.FromSlash(plan.EffectivePath))
		if err := safeAbsoluteComponents(evaluated, temp); err != nil {
			return err
		}
		sourceInfo, sourceErr := os.Lstat(source)
		tempInfo, tempErr := os.Lstat(temp)
		if sourceErr == nil && tempErr == nil {
			if otherSource, published := targetSources[source]; published && otherSource != source {
				otherTemp := temps[otherSource]
				if _, otherTempErr := os.Lstat(otherTemp); os.IsNotExist(otherTempErr) && sourceInfo.Mode().IsRegular() && sourceInfo.Size() == moving[otherSource].Size && tempInfo.Mode().IsRegular() && tempInfo.Size() == plan.Size {
					continue
				}
			}
			return fmt.Errorf("fsafe: source and temporary both exist")
		}
		if sourceErr != nil && !os.IsNotExist(sourceErr) {
			return sourceErr
		}
		if tempErr != nil && !os.IsNotExist(tempErr) {
			return tempErr
		}
		if sourceErr != nil && tempErr != nil {
			targetInfo, targetErr := os.Lstat(target)
			if targetErr == nil && targetInfo.Mode().IsRegular() && targetInfo.Size() == plan.Size {
				if _, expectedFinal := targetSources[target]; expectedFinal {
					completed[source] = true
					continue
				}
				return fmt.Errorf("fsafe: target collision")
			}
			if targetErr != nil && !os.IsNotExist(targetErr) {
				return targetErr
			}
			if targetErr == nil {
				return fmt.Errorf("fsafe: target collision")
			}
		}
		if sourceErr == nil {
			if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() || sourceInfo.Size() != plan.Size {
				return fmt.Errorf("fsafe: source content mismatch")
			}
		}
		if tempErr == nil {
			if tempInfo.Mode()&os.ModeSymlink != 0 || !tempInfo.Mode().IsRegular() || tempInfo.Size() != plan.Size {
				return fmt.Errorf("fsafe: temporary content mismatch")
			}
		}
		if targetInfo, targetErr := os.Lstat(target); targetErr == nil {
			if targetInfo.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("fsafe: target is symlink")
			}
			if _, targetIsMovingSource := moving[target]; !targetIsMovingSource {
				return fmt.Errorf("fsafe: target collision")
			}
		} else if !os.IsNotExist(targetErr) {
			return targetErr
		}
	}
	for source, temp := range temps {
		if completed[source] {
			continue
		}
		if _, err := os.Lstat(temp); err == nil {
			continue
		}
		if err := os.Rename(source, temp); err != nil {
			return err
		}
	}
	for source, plan := range moving {
		temp, target := temps[source], filepath.Join(evaluated, filepath.FromSlash(plan.EffectivePath))
		if completed[source] {
			continue
		}
		if _, err := os.Lstat(temp); os.IsNotExist(err) {
			if targetInfo, targetErr := os.Lstat(target); targetErr == nil && targetInfo.Mode().IsRegular() && targetInfo.Size() == plan.Size {
				if _, expectedFinal := targetSources[target]; expectedFinal {
					continue
				}
				return fmt.Errorf("fsafe: target collision")
			}
			return err
		} else if err != nil {
			return err
		}
		if err := safeAbsoluteComponents(evaluated, filepath.Dir(target)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o770); err != nil {
			return err
		}
		if targetInfo, targetErr := os.Lstat(target); targetErr == nil {
			if targetInfo.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("fsafe: target is symlink")
			}
			if _, sourceTarget := moving[target]; !sourceTarget {
				return fmt.Errorf("fsafe: target collision")
			}
		} else if !os.IsNotExist(targetErr) {
			return targetErr
		}
		if err := os.Rename(temp, target); err != nil {
			return err
		}
	}
	for _, plan := range plans {
		if plan.Priority != 0 {
			continue
		}
		candidate := filepath.Join(evaluated, filepath.FromSlash(plan.OriginalPath))
		if err := safeAbsoluteComponents(evaluated, candidate); err != nil {
			return err
		}
		info, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != plan.Size {
			return fmt.Errorf("fsafe: exclusion content mismatch")
		}
		if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func safePlanComponents(root, relative string) error {
	return safeAbsoluteComponents(root, filepath.Join(root, filepath.FromSlash(relative)))
}

func safeAbsoluteComponents(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || outsideRoot(relative) {
		return fmt.Errorf("fsafe: plan path escapes root")
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, part := range splitPath(relative) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("fsafe: plan path contains symlink")
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
	}
	return nil
}

func splitPath(value string) []string {
	parts := make([]string, 0)
	for value != "." && value != "" {
		parent, base := filepath.Dir(value), filepath.Base(value)
		parts = append([]string{base}, parts...)
		if parent == value {
			break
		}
		value = parent
	}
	return parts
}
