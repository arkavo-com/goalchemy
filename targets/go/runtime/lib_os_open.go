package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsOpen(name string, flag int, perm uint32) (int64, int) {
	return nativeos.Open(name, flag, perm)
}
