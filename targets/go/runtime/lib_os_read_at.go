package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsReadAt(file int64, offset int64, max int) ([]byte, bool, int) {
	return nativeos.ReadAt(file, offset, max)
}
