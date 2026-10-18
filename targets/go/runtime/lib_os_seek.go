package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsSeek(file int64, offset int64, whence int) (int64, int) {
	return nativeos.Seek(file, offset, whence)
}
