package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsWrite(file int64, data []byte) (int, int) { return nativeos.Write(file, data) }
