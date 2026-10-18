package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsRemove(name string) int { return nativeos.Remove(name) }
