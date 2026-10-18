package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsStat(name string) (int64, int, bool, int) { return nativeos.Stat(name) }
