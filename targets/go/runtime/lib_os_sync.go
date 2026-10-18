package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsSync(file int64) int { return nativeos.Sync(file) }
