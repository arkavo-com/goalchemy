package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsClose(file int64) int { return nativeos.Close(file) }
