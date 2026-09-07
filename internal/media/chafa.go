package media

import (
	"os/exec"
)

func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func runChafa(bin string, cols, rows int, path string) (string, error) {
	cmd := exec.Command(bin,
		"--format", "symbols",
		"--symbols", "block+border+space",
		"--scale", "max",
		"--view-size", itoa(cols)+"x"+itoa(rows),
		"--animate", "false",
		path)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
