// Command atcode-sensor is the small privileged component used by the opt-in
// Linux runtime sensor. It accepts only the reviewed helper arguments and does
// not expose a public control socket or execute workspace commands.
package main

import (
	"fmt"
	"os"

	"github.com/aistatine/sonut/internal/security/sensor"
)

func main() {
	if err := sensor.RunHelper(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "atcode-sensor:", err)
		os.Exit(1)
	}
}
