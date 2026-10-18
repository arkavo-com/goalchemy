package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsCreateTemp(dir string, prefix string, suffix string) (int64, string, int) {
	return nativeos.CreateTemp(dir, prefix, suffix)
}
