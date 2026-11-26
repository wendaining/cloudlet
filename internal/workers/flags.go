package workers

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const MaxCompatibilityFlags = 64

var compatibilityFlagName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// normalizeCompatibilityFlags validates and canonicalizes explicitly declared
// flags. The pinned workerd executable checks their runtime semantics.
func normalizeCompatibilityFlags(flags []string) ([]string, error) {
	if len(flags) > MaxCompatibilityFlags {
		return nil, fmt.Errorf("at most %d Worker compatibility flags are allowed", MaxCompatibilityFlags)
	}
	result := slices.Clone(flags)
	slices.Sort(result)
	for index, flag := range result {
		if !compatibilityFlagName.MatchString(flag) {
			return nil, fmt.Errorf("invalid Worker compatibility flag %q", flag)
		}
		if index > 0 && flag == result[index-1] {
			return nil, fmt.Errorf("duplicate Worker compatibility flag %q", flag)
		}
	}
	return result, nil
}

func renderCompatibilityFlags(flags []string) string {
	if len(flags) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(flags))
	for _, flag := range flags {
		quoted = append(quoted, strconv.Quote(flag))
	}
	return "  compatibilityFlags = [" + strings.Join(quoted, ", ") + "],\n"
}
