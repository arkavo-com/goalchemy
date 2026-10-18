package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsRead(file int64, max int) ([]byte, bool, int) { return nativeos.Read(file, max) }
