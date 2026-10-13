package main

import "github.com/eugenioenko/goalchemy/std/errors"

func check(s string) (err error) {
	for i := 0; i < len(s); i++ {
		if s[i] == ';' {
			err = errors.New("semicolon")
			continue
		}
	}
	return err
}

func main() {
	println(check("a;b").Error(), check("ab") == nil)
}
