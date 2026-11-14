package scrape

import "fmt"

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func filterTargets(all []Target, only *[20]byte) []Target {
	if only == nil {
		return all
	}
	var out []Target
	for _, t := range all {
		if t.Hash == *only {
			out = append(out, t)
		}
	}
	return out
}
