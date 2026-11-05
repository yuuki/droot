// Command gendocs writes the command line reference (cli.md) and the man
// page (droot.1) of droot, generated from its command definitions, to the
// directory given as argument (`make docs` uses documentation/).
package main

import (
	"fmt"
	"os"

	"github.com/yuuki/droot/internal/commands"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gendocs DIR")
		os.Exit(2)
	}
	if err := commands.WriteDocs(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}
