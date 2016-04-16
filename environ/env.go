package environ

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const DROOT_ENV_FILE_PATH = "/.drootenv"

func GetEnvironFromEnvFile(filename string) ([]string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var env []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		l := strings.Trim(scanner.Text(), " \n\t")
		if len(l) == 0 {
			continue
		}
		if len(strings.Split(l, "=")) != 2 { // line should be `key=value`
			continue
		}
		env = append(env, l)
	}

	return env, nil
}

// Merge env string array. `dst` and `src` must be KEY=VALUE format.
// If the items of `dst` and `src` has the same KEY, those of `src` overrides those of `dst`.
func MergeEnviron(dst []string, src []string) ([]string, error) {
	for _, s := range src {
		kv := strings.SplitN(s, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("Invalid env format: %s", s)
		}
		sk := kv[0]

		copied := false

		for i, d := range dst {
			kv = strings.SplitN(d, "=", 2)
			if len(kv) != 2 {
				return nil, fmt.Errorf("Invalid env format: %s", d)
			}
			dk := kv[0]
			if sk == dk {
				dst[i] = s
				copied = true
			}
		}

		if !copied {
			dst = append(dst, s)
			copied = false
		}
	}

	return dst, nil
}
