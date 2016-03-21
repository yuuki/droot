package environ

import (
	"bufio"
	"os"
	"strings"
)

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
